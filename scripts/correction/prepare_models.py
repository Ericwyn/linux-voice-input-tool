#!/usr/bin/env python3
"""Download, export, quantize, and verify the two BERT CSC checkpoints.

This is a development/setup tool. The shipped Go application only needs the
resulting model.int8.onnx, vocab.txt, and libonnxruntime; it does not import
Python, Transformers, or PyTorch at runtime.
"""

from __future__ import annotations

import argparse
import json
import shutil
import sys
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import onnxruntime as ort
import torch
from onnxruntime.quantization import QuantType, quantize_dynamic
from transformers import AutoModelForMaskedLM, AutoTokenizer


@dataclass(frozen=True)
class ModelSpec:
    name: str
    repository: str
    directory: str


MODEL_SPECS = {
    "macbert-mdcspell-v2": ModelSpec(
        name="macbert-mdcspell-v2",
        repository="Macropodus/macbert4mdcspell_v2",
        directory="macbert-mdcspell-v2",
    ),
    "macbert4csc": ModelSpec(
        name="macbert4csc",
        repository="shibing624/macbert4csc-base-chinese",
        directory="macbert4csc",
    ),
}


class LogitsOnly(torch.nn.Module):
    """Give torch.onnx.export a stable tensor-only output."""

    def __init__(self, model: torch.nn.Module) -> None:
        super().__init__()
        self.model = model

    def forward(
        self,
        input_ids: torch.Tensor,
        attention_mask: torch.Tensor,
        token_type_ids: torch.Tensor,
    ) -> torch.Tensor:
        return self.model(
            input_ids=input_ids,
            attention_mask=attention_mask,
            token_type_ids=token_type_ids,
            return_dict=True,
        ).logits


def parse_args() -> argparse.Namespace:
    default_output = Path.home() / ".linux-voice-input" / "models" / "correction"
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--models",
        default=",".join(MODEL_SPECS),
        help="comma-separated model aliases: " + ", ".join(MODEL_SPECS),
    )
    parser.add_argument("--output", type=Path, default=default_output)
    parser.add_argument("--opset", type=int, default=17)
    parser.add_argument("--force", action="store_true", help="replace an existing prepared model")
    parser.add_argument("--keep-fp32", action="store_true")
    parser.add_argument("--local-files-only", action="store_true")
    return parser.parse_args()


def selected_specs(value: str) -> list[ModelSpec]:
    selected: list[ModelSpec] = []
    for raw_name in value.split(","):
        name = raw_name.strip()
        if not name:
            continue
        if name not in MODEL_SPECS:
            raise ValueError(f"unknown model alias {name!r}")
        if MODEL_SPECS[name] not in selected:
            selected.append(MODEL_SPECS[name])
    if not selected:
        raise ValueError("no models selected")
    return selected


def export_fp32(
    spec: ModelSpec,
    destination: Path,
    opset: int,
    local_files_only: bool,
) -> tuple[AutoTokenizer, int]:
    print(f"[{spec.name}] loading {spec.repository}", flush=True)
    tokenizer = AutoTokenizer.from_pretrained(
        spec.repository,
        use_fast=False,
        local_files_only=local_files_only,
    )
    model = AutoModelForMaskedLM.from_pretrained(
        spec.repository,
        local_files_only=local_files_only,
    )
    model.eval()
    model.to("cpu")

    encoded = tokenizer(
        "少先队员应该为老人让座",
        return_tensors="pt",
        truncation=True,
        max_length=32,
    )
    input_ids = encoded["input_ids"]
    attention_mask = encoded["attention_mask"]
    token_type_ids = encoded.get("token_type_ids", torch.zeros_like(input_ids))
    wrapper = LogitsOnly(model)

    print(f"[{spec.name}] exporting fp32 ONNX -> {destination}", flush=True)
    torch.onnx.export(
        wrapper,
        (input_ids, attention_mask, token_type_ids),
        str(destination),
        input_names=["input_ids", "attention_mask", "token_type_ids"],
        output_names=["logits"],
        dynamic_axes={
            "input_ids": {0: "batch", 1: "sequence"},
            "attention_mask": {0: "batch", 1: "sequence"},
            "token_type_ids": {0: "batch", 1: "sequence"},
            "logits": {0: "batch", 1: "sequence"},
        },
        opset_version=opset,
        do_constant_folding=True,
        dynamo=False,
    )
    vocab_size = int(model.config.vocab_size)
    del wrapper
    del model
    return tokenizer, vocab_size


def verify_model(model_path: Path, tokenizer: AutoTokenizer, vocab_size: int) -> dict[str, object]:
    session = ort.InferenceSession(str(model_path), providers=["CPUExecutionProvider"])
    encoded = tokenizer("少先队员因该为老人让坐", return_tensors="np")
    input_ids = encoded["input_ids"].astype(np.int64)
    attention_mask = encoded["attention_mask"].astype(np.int64)
    token_type_ids = encoded.get("token_type_ids", np.zeros_like(input_ids)).astype(np.int64)
    started = time.perf_counter()
    outputs = session.run(
        [session.get_outputs()[0].name],
        {
            "input_ids": input_ids,
            "attention_mask": attention_mask,
            "token_type_ids": token_type_ids,
        },
    )
    elapsed_ms = round((time.perf_counter() - started) * 1000, 2)
    shape = list(outputs[0].shape)
    expected_shape = [1, int(input_ids.shape[1]), vocab_size]
    if shape != expected_shape:
        raise RuntimeError(f"unexpected logits shape {shape}, want {expected_shape}")
    return {
        "inputs": [item.name for item in session.get_inputs()],
        "output": session.get_outputs()[0].name,
        "shape": shape,
        "verification_ms": elapsed_ms,
        "onnxruntime": ort.__version__,
    }


def prepare_model(spec: ModelSpec, args: argparse.Namespace) -> None:
    final_directory = args.output / spec.directory
    final_model = final_directory / "model.int8.onnx"
    final_vocab = final_directory / "vocab.txt"
    if not args.force and final_model.is_file() and final_vocab.is_file():
        print(f"[{spec.name}] already prepared at {final_directory}; skipping", flush=True)
        return

    args.output.mkdir(parents=True, exist_ok=True)
    temporary_parent = Path(tempfile.mkdtemp(prefix=f".{spec.directory}-", dir=args.output))
    try:
        fp32_path = temporary_parent / "model.fp32.onnx"
        int8_path = temporary_parent / "model.int8.onnx"
        tokenizer, vocab_size = export_fp32(
            spec,
            fp32_path,
            args.opset,
            args.local_files_only,
        )
        tokenizer.save_pretrained(temporary_parent)
        if not (temporary_parent / "vocab.txt").is_file():
            raise RuntimeError("tokenizer did not produce vocab.txt")

        print(f"[{spec.name}] dynamic INT8 quantization", flush=True)
        quantize_dynamic(
            model_input=str(fp32_path),
            model_output=str(int8_path),
            per_channel=True,
            weight_type=QuantType.QInt8,
        )
        verification = verify_model(int8_path, tokenizer, vocab_size)
        manifest = {
            "name": spec.name,
            "source": spec.repository,
            "format": "bert-mlm-onnx-int8",
            "vocab_size": vocab_size,
            "model_bytes": int8_path.stat().st_size,
            "opset": args.opset,
            **verification,
        }
        (temporary_parent / "manifest.json").write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        if not args.keep_fp32:
            fp32_path.unlink()

        if final_directory.exists():
            if not args.force:
                raise RuntimeError(f"destination already exists: {final_directory}")
            shutil.rmtree(final_directory)
        temporary_parent.rename(final_directory)
        print(
            f"[{spec.name}] ready: {final_model} "
            f"({manifest['model_bytes'] / 1024 / 1024:.1f} MiB), "
            f"verify={manifest['verification_ms']}ms",
            flush=True,
        )
    except Exception:
        shutil.rmtree(temporary_parent, ignore_errors=True)
        raise


def main() -> int:
    args = parse_args()
    try:
        specs = selected_specs(args.models)
        for spec in specs:
            prepare_model(spec, args)
    except Exception as error:
        print(f"prepare_models: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
