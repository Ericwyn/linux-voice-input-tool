package input

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--clipboard-owner-helper" {
		if err := ServeClipboardOwner(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stdout, "error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestClipboardSnapshotClonePreservesBinaryData(t *testing.T) {
	original := clipboardSnapshot{
		HadOwner: true,
		Payloads: []clipboardPayload{{
			Target: 11,
			Type:   12,
			Format: 8,
			Data:   []byte{0x89, 'P', 'N', 'G', 0x00, 0xff},
		}},
	}
	clone := original.clone()
	if !clone.HadOwner || len(clone.Payloads) != 1 || !bytes.Equal(clone.Payloads[0].Data, original.Payloads[0].Data) {
		t.Fatalf("clone = %#v, want a binary-equivalent snapshot", clone)
	}
	clone.Payloads[0].Data[0] = 0
	if original.Payloads[0].Data[0] != 0x89 {
		t.Fatal("clone shares its payload buffer with the original")
	}
}

func TestShouldSnapshotTarget(t *testing.T) {
	for _, target := range []string{"image/png", "UTF8_STRING", "text/html", "x-special/gnome-copied-files"} {
		if !shouldSnapshotTarget(target) {
			t.Errorf("shouldSnapshotTarget(%q) = false, want true", target)
		}
	}
	for _, target := range []string{"", "TARGETS", "MULTIPLE", "SAVE_TARGETS", "DELETE", "PIXMAP", "_NET_MAX_SELECTION_SIZE"} {
		if shouldSnapshotTarget(target) {
			t.Errorf("shouldSnapshotTarget(%q) = true, want false", target)
		}
	}
}

func TestEncodeDecodeAtoms(t *testing.T) {
	want := []xproto.Atom{1, 42, 0x01020304}
	got := decodeAtoms(encodeAtoms(want))
	if len(got) != len(want) {
		t.Fatalf("decoded atoms = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded atoms = %v, want %v", got, want)
		}
	}
}

// This integration test uses a private X11 selection atom, never CLIPBOARD, so
// it cannot disturb the user's real clipboard. It is skipped on headless test
// runners and exercises both direct text transfer and INCR image transfer when
// an X server is available.
func TestX11ClipboardOwnerRoundTripOnPrivateSelection(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY is not set")
	}

	ownerConn, err := newXClipboardConnection()
	if err != nil {
		t.Skipf("X11 is unavailable: %v", err)
	}
	selectionName := fmt.Sprintf("_LINUX_VOICE_INPUT_TEST_%d", time.Now().UnixNano())
	selection := internTestAtom(t, ownerConn, selectionName)
	ownerConn.atoms.clipboard = selection

	owner, err := startClipboardOwnerWithConnection(ownerConn, "中文 clipboard test")
	if err != nil {
		ownerConn.close()
		t.Fatalf("start private clipboard owner: %v", err)
	}
	defer stopTestClipboardOwner(t, owner)

	requester, err := newXClipboardConnection()
	if err != nil {
		t.Fatalf("create clipboard requester: %v", err)
	}
	defer requester.close()
	requester.atoms.clipboard = internTestAtom(t, requester, selectionName)

	textPayload, err := requester.convertSelection(requester.atoms.utf8String, maxClipboardSnapshotBytes)
	if err != nil {
		t.Fatalf("read UTF8_STRING: %v", err)
	}
	if got, want := string(textPayload.Data), "中文 clipboard test"; got != want {
		t.Fatalf("text payload = %q, want %q", got, want)
	}
	multiple := requestMultipleTargets(t, requester, []xproto.Atom{
		requester.atoms.utf8String,
		requester.atoms.textPlain,
	})
	for target, data := range multiple {
		if got, want := string(data), "中文 clipboard test"; got != want {
			t.Fatalf("MULTIPLE target %d = %q, want %q", target, got, want)
		}
	}

	imageTarget := internTestAtom(t, ownerConn, "image/png")
	htmlTarget := internTestAtom(t, ownerConn, "text/html")
	largeImage := make([]byte, owner.maxPayload+32*1024)
	for i := range largeImage {
		largeImage[i] = byte(i * 31)
	}
	imageSnapshot := clipboardSnapshot{
		HadOwner: true,
		Payloads: []clipboardPayload{{
			Target: imageTarget,
			Type:   imageTarget,
			Format: 8,
			Data:   largeImage,
		}, {
			Target: htmlTarget,
			Type:   htmlTarget,
			Format: 8,
			Data:   []byte("<b>clipboard</b>"),
		}},
	}
	if err := owner.restore(imageSnapshot); err != nil {
		t.Fatalf("replace private clipboard with image: %v", err)
	}

	imagePayload, err := requester.convertSelection(internTestAtom(t, requester, "image/png"), maxClipboardSnapshotBytes)
	if err != nil {
		t.Fatalf("read incremental image/png: %v", err)
	}
	if !bytes.Equal(imagePayload.Data, largeImage) {
		t.Fatalf("image payload length/content mismatch: got %d bytes, want %d", len(imagePayload.Data), len(largeImage))
	}

	captured, err := captureClipboardWithConnection(requester)
	if err != nil {
		t.Fatalf("capture private image clipboard: %v", err)
	}
	if !captured.HadOwner || len(captured.Payloads) != 2 {
		t.Fatalf("captured private clipboard = owner:%v payloads:%d", captured.HadOwner, len(captured.Payloads))
	}
	capturedByTarget := make(map[xproto.Atom][]byte, len(captured.Payloads))
	for _, payload := range captured.Payloads {
		capturedByTarget[payload.Target] = payload.Data
	}
	if !bytes.Equal(capturedByTarget[imageTarget], largeImage) {
		t.Fatalf("captured image payload length/content mismatch: got %d bytes, want %d", len(capturedByTarget[imageTarget]), len(largeImage))
	}
	if got, want := string(capturedByTarget[htmlTarget]), "<b>clipboard</b>"; got != want {
		t.Fatalf("captured HTML payload = %q, want %q", got, want)
	}

	// Simulate the standalone process handoff. The replacement
	// owner uses the original timestamp, which allows a safe handoff but cannot
	// overwrite a newer clipboard owner if the user copies concurrently.
	if err := owner.handoffToHelper(); err != nil {
		t.Fatalf("hand off clipboard to helper process: %v", err)
	}

	helperImage, err := requester.convertSelection(imageTarget, maxClipboardSnapshotBytes)
	if err != nil {
		t.Fatalf("read image from handed-off owner: %v", err)
	}
	if !bytes.Equal(helperImage.Data, largeImage) {
		t.Fatalf("handed-off image payload length/content mismatch: got %d bytes, want %d", len(helperImage.Data), len(largeImage))
	}
	if err := xproto.SetSelectionOwnerChecked(
		requester.conn,
		xproto.WindowNone,
		selection,
		xproto.TimeCurrentTime,
	).Check(); err != nil {
		t.Fatalf("release handed-off selection: %v", err)
	}
}

func TestRestoreDoesNotOverwriteNewerPrivateSelection(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY is not set")
	}

	ownerConn, err := newXClipboardConnection()
	if err != nil {
		t.Skipf("X11 is unavailable: %v", err)
	}
	selectionName := fmt.Sprintf("_LINUX_VOICE_INPUT_RACE_TEST_%d", time.Now().UnixNano())
	selection := internTestAtom(t, ownerConn, selectionName)
	ownerConn.atoms.clipboard = selection
	owner, err := startClipboardOwnerWithConnection(ownerConn, "old")
	if err != nil {
		ownerConn.close()
		t.Fatalf("start old private clipboard owner: %v", err)
	}
	defer stopTestClipboardOwner(t, owner)

	newer, err := newXClipboardConnection()
	if err != nil {
		t.Fatalf("create newer clipboard owner: %v", err)
	}
	defer newer.close()
	newer.atoms.clipboard = internTestAtom(t, newer, selectionName)
	newerTime, err := newer.serverTimestamp()
	if err != nil {
		t.Fatalf("get newer owner timestamp: %v", err)
	}
	if err := xproto.SetSelectionOwnerChecked(newer.conn, newer.window, selection, newerTime).Check(); err != nil {
		t.Fatalf("claim newer private clipboard: %v", err)
	}

	// This may run before the old owner's event loop processes SelectionClear.
	// restore uses the old acquisition timestamp, so it cannot clear the newer
	// selection even in that narrow race.
	if err := owner.restore(clipboardSnapshot{HadOwner: false}); err != nil {
		t.Fatalf("restore old empty clipboard state: %v", err)
	}
	current, err := xproto.GetSelectionOwner(newer.conn, selection).Reply()
	if err != nil {
		t.Fatalf("read current private clipboard owner: %v", err)
	}
	if current.Owner != newer.window {
		t.Fatalf("restore overwrote newer clipboard owner: got %d, want %d", current.Owner, newer.window)
	}
	if err := xproto.SetSelectionOwnerChecked(newer.conn, xproto.WindowNone, selection, newerTime).Check(); err != nil {
		t.Fatalf("release newer private clipboard: %v", err)
	}
}

func internTestAtom(t *testing.T, x *xClipboardConnection, name string) xproto.Atom {
	t.Helper()
	reply, err := xproto.InternAtom(x.conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		t.Fatalf("intern atom %s: %v", name, err)
	}
	return reply.Atom
}

func requestMultipleTargets(t *testing.T, x *xClipboardConnection, targets []xproto.Atom) map[xproto.Atom][]byte {
	t.Helper()
	suffix := time.Now().UnixNano()
	multipleProperty := internTestAtom(t, x, fmt.Sprintf("_LINUX_VOICE_INPUT_MULTI_%d", suffix))
	destinations := make([]xproto.Atom, len(targets))
	pairs := make([]byte, len(targets)*8)
	for i, target := range targets {
		destination := internTestAtom(t, x, fmt.Sprintf("_LINUX_VOICE_INPUT_MULTI_%d_%d", suffix, i))
		destinations[i] = destination
		xgb.Put32(pairs[i*8:], uint32(target))
		xgb.Put32(pairs[i*8+4:], uint32(destination))
	}
	if err := changeProperty(x.conn, x.window, multipleProperty, x.atoms.atomPair, 32, pairs); err != nil {
		t.Fatalf("write MULTIPLE request property: %v", err)
	}
	if err := xproto.ConvertSelectionChecked(
		x.conn,
		x.window,
		x.atoms.clipboard,
		x.atoms.multiple,
		multipleProperty,
		xproto.TimeCurrentTime,
	).Check(); err != nil {
		t.Fatalf("request MULTIPLE conversion: %v", err)
	}
	for {
		event, err := x.waitEvent(clipboardRequestTimeout)
		if err != nil {
			t.Fatalf("wait for MULTIPLE conversion: %v", err)
		}
		notify, ok := event.(xproto.SelectionNotifyEvent)
		if !ok || notify.Requestor != x.window || notify.Target != x.atoms.multiple {
			continue
		}
		if notify.Property != multipleProperty {
			t.Fatalf("MULTIPLE conversion property = %d, want %d", notify.Property, multipleProperty)
		}
		break
	}

	result := make(map[xproto.Atom][]byte, len(targets))
	for i, target := range targets {
		reply, err := xproto.GetProperty(
			x.conn,
			true,
			x.window,
			destinations[i],
			xproto.GetPropertyTypeAny,
			0,
			uint32(maxClipboardSnapshotBytes/4+1),
		).Reply()
		if err != nil {
			t.Fatalf("read MULTIPLE target %d: %v", target, err)
		}
		if reply == nil || reply.Type == xproto.AtomNone || reply.BytesAfter != 0 {
			t.Fatalf("MULTIPLE target %d returned an invalid property", target)
		}
		result[target] = append([]byte(nil), reply.Value...)
	}
	return result
}

func stopTestClipboardOwner(t *testing.T, owner *clipboardOwner) {
	t.Helper()
	if err := owner.restore(clipboardSnapshot{HadOwner: false}); err != nil {
		t.Errorf("release private clipboard: %v", err)
	}
	select {
	case <-owner.done:
	case <-time.After(time.Second):
		owner.close()
		t.Error("private clipboard owner did not stop")
	}
}
