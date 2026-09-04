package input

import (
	"bufio"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
)

const (
	clipboardRequestTimeout   = 2 * time.Second
	clipboardPersistTimeout   = 5 * time.Second
	clipboardTransferTimeout  = 10 * time.Second
	maxClipboardSnapshotBytes = 64 << 20
	maxClipboardTargets       = 128
	maxDirectPropertyBytes    = 240 << 10
)

var (
	errClipboardManagerUnavailable = errors.New("X11 clipboard manager unavailable")
	errSelectionTargetRefused      = errors.New("selection owner refused the target")
)

// clipboardPayload is one representation advertised by an X11 selection
// owner. Type and Format describe the property exactly as the original owner
// returned it; Data is deliberately binary-safe so images and other non-text
// targets survive a restore.
type clipboardPayload struct {
	Target xproto.Atom
	Type   xproto.Atom
	Format byte
	Data   []byte
}

type clipboardSnapshot struct {
	HadOwner bool
	Payloads []clipboardPayload
}

func (s clipboardSnapshot) clone() clipboardSnapshot {
	clone := clipboardSnapshot{HadOwner: s.HadOwner}
	clone.Payloads = make([]clipboardPayload, 0, len(s.Payloads))
	for _, payload := range s.Payloads {
		payload.Data = append([]byte(nil), payload.Data...)
		clone.Payloads = append(clone.Payloads, payload)
	}
	return clone
}

type clipboardAtoms struct {
	clipboard         xproto.Atom
	targets           xproto.Atom
	multiple          xproto.Atom
	timestamp         xproto.Atom
	incr              xproto.Atom
	atomPair          xproto.Atom
	null              xproto.Atom
	saveTargets       xproto.Atom
	clipboardManager  xproto.Atom
	utf8String        xproto.Atom
	text              xproto.Atom
	textPlain         xproto.Atom
	textPlainUTF8     xproto.Atom
	dataProperty      xproto.Atom
	timestampProperty xproto.Atom
}

var clipboardAtomNames = []string{
	"CLIPBOARD",
	"TARGETS",
	"MULTIPLE",
	"TIMESTAMP",
	"INCR",
	"ATOM_PAIR",
	"NULL",
	"SAVE_TARGETS",
	"CLIPBOARD_MANAGER",
	"UTF8_STRING",
	"TEXT",
	"text/plain",
	"text/plain;charset=utf-8",
	"_LINUX_VOICE_INPUT_CLIPBOARD_DATA",
	"_LINUX_VOICE_INPUT_TIMESTAMP",
}

type xClipboardConnection struct {
	conn       *xgb.Conn
	window     xproto.Window
	atoms      clipboardAtoms
	maxPayload int
	closeOnce  sync.Once
}

func newXClipboardConnection() (*xClipboardConnection, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connect to X server: %w", err)
	}

	closeOnError := true
	defer func() {
		if closeOnError {
			conn.Close()
		}
	}()

	atomsByName, err := internClipboardAtoms(conn)
	if err != nil {
		return nil, err
	}

	window, err := xproto.NewWindowId(conn)
	if err != nil {
		return nil, fmt.Errorf("allocate clipboard window: %w", err)
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	if err := xproto.CreateWindowChecked(
		conn,
		screen.RootDepth,
		window,
		screen.Root,
		0,
		0,
		1,
		1,
		0,
		xproto.WindowClassInputOutput,
		screen.RootVisual,
		xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange},
	).Check(); err != nil {
		return nil, fmt.Errorf("create clipboard window: %w", err)
	}

	maxPayload := int(setup.MaximumRequestLength)*4 - 24
	if maxPayload <= 0 || maxPayload > maxDirectPropertyBytes {
		maxPayload = maxDirectPropertyBytes
	}

	x := &xClipboardConnection{
		conn:       conn,
		window:     window,
		maxPayload: maxPayload,
		atoms: clipboardAtoms{
			clipboard:         atomsByName["CLIPBOARD"],
			targets:           atomsByName["TARGETS"],
			multiple:          atomsByName["MULTIPLE"],
			timestamp:         atomsByName["TIMESTAMP"],
			incr:              atomsByName["INCR"],
			atomPair:          atomsByName["ATOM_PAIR"],
			null:              atomsByName["NULL"],
			saveTargets:       atomsByName["SAVE_TARGETS"],
			clipboardManager:  atomsByName["CLIPBOARD_MANAGER"],
			utf8String:        atomsByName["UTF8_STRING"],
			text:              atomsByName["TEXT"],
			textPlain:         atomsByName["text/plain"],
			textPlainUTF8:     atomsByName["text/plain;charset=utf-8"],
			dataProperty:      atomsByName["_LINUX_VOICE_INPUT_CLIPBOARD_DATA"],
			timestampProperty: atomsByName["_LINUX_VOICE_INPUT_TIMESTAMP"],
		},
	}
	closeOnError = false
	return x, nil
}

func internClipboardAtoms(conn *xgb.Conn) (map[string]xproto.Atom, error) {
	cookies := make(map[string]xproto.InternAtomCookie, len(clipboardAtomNames))
	for _, name := range clipboardAtomNames {
		cookies[name] = xproto.InternAtom(conn, false, uint16(len(name)), name)
	}

	atoms := make(map[string]xproto.Atom, len(cookies))
	for _, name := range clipboardAtomNames {
		reply, err := cookies[name].Reply()
		if err != nil {
			return nil, fmt.Errorf("intern clipboard atom %s: %w", name, err)
		}
		atoms[name] = reply.Atom
	}
	return atoms, nil
}

func (x *xClipboardConnection) close() {
	x.closeOnce.Do(func() {
		x.conn.Close()
	})
}

func (x *xClipboardConnection) waitEvent(timeout time.Duration) (xgb.Event, error) {
	deadline := time.Now().Add(timeout)
	for {
		event, xerr := x.conn.PollForEvent()
		if xerr != nil {
			return nil, fmt.Errorf("X11 event error: %w", xerr)
		}
		if event != nil {
			return event, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s", timeout)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (x *xClipboardConnection) atomName(atom xproto.Atom) (string, error) {
	reply, err := xproto.GetAtomName(x.conn, atom).Reply()
	if err != nil {
		return "", err
	}
	return reply.Name, nil
}

func captureClipboard() (clipboardSnapshot, error) {
	x, err := newXClipboardConnection()
	if err != nil {
		return clipboardSnapshot{}, err
	}
	defer x.close()
	return captureClipboardWithConnection(x)
}

func captureClipboardWithConnection(x *xClipboardConnection) (clipboardSnapshot, error) {
	ownerReply, err := xproto.GetSelectionOwner(x.conn, x.atoms.clipboard).Reply()
	if err != nil {
		return clipboardSnapshot{}, fmt.Errorf("get clipboard owner: %w", err)
	}
	if ownerReply.Owner == xproto.WindowNone {
		return clipboardSnapshot{HadOwner: false}, nil
	}
	originalOwner := ownerReply.Owner

	targetList, err := x.convertSelection(x.atoms.targets, maxClipboardSnapshotBytes)
	if err != nil {
		return clipboardSnapshot{}, fmt.Errorf("read clipboard TARGETS: %w", err)
	}
	if targetList.Type != xproto.AtomAtom || targetList.Format != 32 || len(targetList.Data)%4 != 0 {
		return clipboardSnapshot{}, fmt.Errorf(
			"invalid clipboard TARGETS property: type=%d format=%d bytes=%d",
			targetList.Type,
			targetList.Format,
			len(targetList.Data),
		)
	}

	targets := decodeAtoms(targetList.Data)
	if len(targets) > maxClipboardTargets {
		return clipboardSnapshot{}, fmt.Errorf("clipboard advertises too many targets: %d", len(targets))
	}
	payloads, err := captureAdvertisedTargets(targets, x.atomName, x.convertSelection)
	if err != nil {
		return clipboardSnapshot{}, err
	}
	snapshot := clipboardSnapshot{HadOwner: true, Payloads: payloads}

	ownerReply, err = xproto.GetSelectionOwner(x.conn, x.atoms.clipboard).Reply()
	if err != nil {
		return clipboardSnapshot{}, fmt.Errorf("recheck clipboard owner: %w", err)
	}
	if ownerReply.Owner != originalOwner {
		return clipboardSnapshot{}, errors.New("clipboard changed while it was being backed up")
	}

	return snapshot, nil
}

// captureAdvertisedTargets reads the payload-bearing formats returned by
// TARGETS. X11 owners in the wild occasionally advertise a format and then
// reject a conversion request for that same format. Such a target contains no
// retrievable data, so skip it while retaining every representation the owner
// can actually serve.
func captureAdvertisedTargets(
	targets []xproto.Atom,
	atomName func(xproto.Atom) (string, error),
	convertSelection func(xproto.Atom, int) (clipboardPayload, error),
) ([]clipboardPayload, error) {
	payloads := make([]clipboardPayload, 0, len(targets))
	seen := make(map[xproto.Atom]struct{})
	totalBytes := 0
	refusedTargets := 0
	for _, target := range targets {
		if target == xproto.AtomNone {
			continue
		}
		if _, duplicate := seen[target]; duplicate {
			continue
		}
		seen[target] = struct{}{}

		name, err := atomName(target)
		if err != nil {
			return nil, fmt.Errorf("resolve clipboard target %d: %w", target, err)
		}
		if !shouldSnapshotTarget(name) {
			continue
		}

		remaining := maxClipboardSnapshotBytes - totalBytes
		payload, err := convertSelection(target, remaining)
		if errors.Is(err, errSelectionTargetRefused) {
			refusedTargets++
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read clipboard target %s: %w", name, err)
		}
		if err := validatePayload(payload); err != nil {
			return nil, fmt.Errorf("read clipboard target %s: %w", name, err)
		}
		totalBytes += len(payload.Data)
		if totalBytes > maxClipboardSnapshotBytes {
			return nil, fmt.Errorf(
				"clipboard snapshot exceeds %d MiB",
				maxClipboardSnapshotBytes>>20,
			)
		}
		payload.Target = target
		payloads = append(payloads, payload)
	}

	if len(payloads) == 0 && refusedTargets > 0 {
		return nil, errors.New("clipboard owner refused all advertised payload targets")
	}
	return payloads, nil
}

func (x *xClipboardConnection) convertSelection(target xproto.Atom, limit int) (clipboardPayload, error) {
	if limit < 0 {
		return clipboardPayload{}, errors.New("clipboard snapshot size limit exceeded")
	}
	if err := xproto.DeletePropertyChecked(x.conn, x.window, x.atoms.dataProperty).Check(); err != nil {
		return clipboardPayload{}, fmt.Errorf("clear selection property: %w", err)
	}
	if err := xproto.ConvertSelectionChecked(
		x.conn,
		x.window,
		x.atoms.clipboard,
		target,
		x.atoms.dataProperty,
		xproto.TimeCurrentTime,
	).Check(); err != nil {
		return clipboardPayload{}, fmt.Errorf("request selection conversion: %w", err)
	}

	for {
		event, err := x.waitEvent(clipboardRequestTimeout)
		if err != nil {
			return clipboardPayload{}, err
		}
		notify, ok := event.(xproto.SelectionNotifyEvent)
		if !ok || notify.Requestor != x.window || notify.Selection != x.atoms.clipboard || notify.Target != target {
			continue
		}
		if notify.Property == xproto.AtomNone {
			return clipboardPayload{}, errSelectionTargetRefused
		}
		if notify.Property != x.atoms.dataProperty {
			return clipboardPayload{}, fmt.Errorf("selection owner replied on unexpected property %d", notify.Property)
		}
		break
	}

	reply, err := xproto.GetProperty(
		x.conn,
		false,
		x.window,
		x.atoms.dataProperty,
		xproto.GetPropertyTypeAny,
		0,
		uint32(maxClipboardSnapshotBytes/4+1),
	).Reply()
	if err != nil {
		return clipboardPayload{}, fmt.Errorf("read selection property: %w", err)
	}
	if reply == nil || reply.Type == xproto.AtomNone {
		return clipboardPayload{}, errors.New("selection owner returned no property data")
	}

	if reply.Type != x.atoms.incr {
		if reply.BytesAfter != 0 || len(reply.Value) > limit {
			_ = xproto.DeletePropertyChecked(x.conn, x.window, x.atoms.dataProperty).Check()
			return clipboardPayload{}, errors.New("clipboard target exceeds snapshot size limit")
		}
		if err := xproto.DeletePropertyChecked(x.conn, x.window, x.atoms.dataProperty).Check(); err != nil {
			return clipboardPayload{}, fmt.Errorf("delete selection property: %w", err)
		}
		return clipboardPayload{Type: reply.Type, Format: reply.Format, Data: append([]byte(nil), reply.Value...)}, nil
	}

	return x.readIncrementalSelection(reply, limit)
}

func (x *xClipboardConnection) readIncrementalSelection(initial *xproto.GetPropertyReply, limit int) (clipboardPayload, error) {
	if initial.Format != 32 || len(initial.Value) < 4 {
		return clipboardPayload{}, errors.New("invalid INCR size property")
	}
	expected := int(xgb.Get32(initial.Value))
	if expected > limit {
		_ = xproto.DeletePropertyChecked(x.conn, x.window, x.atoms.dataProperty).Check()
		return clipboardPayload{}, errors.New("incremental clipboard target exceeds snapshot size limit")
	}
	if err := xproto.DeletePropertyChecked(x.conn, x.window, x.atoms.dataProperty).Check(); err != nil {
		return clipboardPayload{}, fmt.Errorf("start incremental selection transfer: %w", err)
	}

	var payload clipboardPayload
	if expected > 0 {
		payload.Data = make([]byte, 0, expected)
	}
	for {
		for {
			event, err := x.waitEvent(clipboardRequestTimeout)
			if err != nil {
				return clipboardPayload{}, fmt.Errorf("wait for incremental clipboard data: %w", err)
			}
			property, ok := event.(xproto.PropertyNotifyEvent)
			if ok && property.Window == x.window && property.Atom == x.atoms.dataProperty && property.State == xproto.PropertyNewValue {
				break
			}
		}

		reply, err := xproto.GetProperty(
			x.conn,
			true,
			x.window,
			x.atoms.dataProperty,
			xproto.GetPropertyTypeAny,
			0,
			uint32(maxClipboardSnapshotBytes/4+1),
		).Reply()
		if err != nil {
			return clipboardPayload{}, fmt.Errorf("read incremental clipboard data: %w", err)
		}
		if reply == nil || reply.BytesAfter != 0 {
			return clipboardPayload{}, errors.New("invalid incremental clipboard chunk")
		}
		if len(reply.Value) == 0 {
			break
		}
		if payload.Type == xproto.AtomNone {
			payload.Type = reply.Type
			payload.Format = reply.Format
		} else if reply.Type != payload.Type || reply.Format != payload.Format {
			return clipboardPayload{}, errors.New("incremental clipboard target changed type or format")
		}
		if len(payload.Data)+len(reply.Value) > limit {
			return clipboardPayload{}, errors.New("incremental clipboard target exceeds snapshot size limit")
		}
		payload.Data = append(payload.Data, reply.Value...)
	}

	if payload.Type == xproto.AtomNone {
		return clipboardPayload{}, errors.New("incremental clipboard target returned no data type")
	}
	return payload, nil
}

func validatePayload(payload clipboardPayload) error {
	switch payload.Format {
	case 8, 16, 32:
		unit := int(payload.Format / 8)
		if len(payload.Data)%unit != 0 {
			return fmt.Errorf("property length %d is not aligned to format %d", len(payload.Data), payload.Format)
		}
	default:
		return fmt.Errorf("unsupported property format %d", payload.Format)
	}
	if payload.Type == xproto.AtomNone {
		return errors.New("property type is None")
	}
	return nil
}

func decodeAtoms(data []byte) []xproto.Atom {
	atoms := make([]xproto.Atom, 0, len(data)/4)
	for offset := 0; offset+4 <= len(data); offset += 4 {
		atoms = append(atoms, xproto.Atom(xgb.Get32(data[offset:])))
	}
	return atoms
}

func encodeAtoms(atoms []xproto.Atom) []byte {
	data := make([]byte, len(atoms)*4)
	for i, atom := range atoms {
		xgb.Put32(data[i*4:], uint32(atom))
	}
	return data
}

var nonPayloadTargets = map[string]struct{}{
	"TARGETS":                 {},
	"MULTIPLE":                {},
	"TIMESTAMP":               {},
	"INCR":                    {},
	"SAVE_TARGETS":            {},
	"DELETE":                  {},
	"INSERT_SELECTION":        {},
	"INSERT_PROPERTY":         {},
	"_NET_MAX_SELECTION_SIZE": {},
	"LENGTH":                  {},
	"LIST_LENGTH":             {},
	"CHARACTER_POSITION":      {},
	"LINE_NUMBER":             {},
	"COLUMN_NUMBER":           {},
	"OWNER_OS":                {},
	"HOST_NAME":               {},
	"USER":                    {},
	"CLASS":                   {},
	"NAME":                    {},
	"CLIENT_WINDOW":           {},
	"PROCESS":                 {},
	"TASK":                    {},
	"PIXMAP":                  {},
	"BITMAP":                  {},
	"DRAWABLE":                {},
	"COLORMAP":                {},
}

func shouldSnapshotTarget(name string) bool {
	_, excluded := nonPayloadTargets[strings.ToUpper(name)]
	return name != "" && !excluded
}

type incrementalTransferKey struct {
	window   xproto.Window
	property xproto.Atom
}

type incrementalTransfer struct {
	payload clipboardPayload
	offset  int
}

type clipboardOwner struct {
	*xClipboardConnection

	mu        sync.RWMutex
	owned     bool
	ownerTime xproto.Timestamp
	payloads  map[xproto.Atom]clipboardPayload
	order     []xproto.Atom
	transfers map[incrementalTransferKey]*incrementalTransfer
	persistCh chan struct{}

	served         chan struct{}
	servedSequence uint64
	lost           chan struct{}
	done           chan struct{}
	lostOnce       sync.Once
}

var activeClipboardOwner struct {
	sync.Mutex
	owner *clipboardOwner
}

func textClipboardSnapshot(atoms clipboardAtoms, text string) clipboardSnapshot {
	data := []byte(text)
	return clipboardSnapshot{
		HadOwner: true,
		Payloads: []clipboardPayload{
			{Target: atoms.utf8String, Type: atoms.utf8String, Format: 8, Data: data},
			{Target: atoms.textPlainUTF8, Type: atoms.textPlainUTF8, Format: 8, Data: data},
			{Target: atoms.textPlain, Type: atoms.textPlain, Format: 8, Data: data},
			{Target: atoms.text, Type: atoms.utf8String, Format: 8, Data: data},
			// Some older X11 clients request only STRING. Keeping the same
			// UTF-8 bytes preserves the previous path's practical compatibility.
			{Target: xproto.AtomString, Type: xproto.AtomString, Format: 8, Data: data},
		},
	}
}

func startClipboardOwner(text string) (*clipboardOwner, error) {
	x, err := newXClipboardConnection()
	if err != nil {
		return nil, err
	}
	owner, err := startClipboardOwnerWithConnection(x, text)
	if err != nil {
		x.close()
		return nil, err
	}
	setActiveClipboardOwner(owner)
	return owner, nil
}

func startClipboardOwnerWithConnection(x *xClipboardConnection, text string) (*clipboardOwner, error) {
	ownerTime, err := x.serverTimestamp()
	if err != nil {
		return nil, err
	}
	return startClipboardOwnerWithSnapshot(x, textClipboardSnapshot(x.atoms, text), ownerTime)
}

func startClipboardOwnerWithSnapshot(x *xClipboardConnection, snapshot clipboardSnapshot, ownerTime xproto.Timestamp) (*clipboardOwner, error) {
	owner := &clipboardOwner{
		xClipboardConnection: x,
		owned:                true,
		ownerTime:            ownerTime,
		transfers:            make(map[incrementalTransferKey]*incrementalTransfer),
		served:               make(chan struct{}),
		lost:                 make(chan struct{}),
		done:                 make(chan struct{}),
	}
	owner.setPayloadsLocked(snapshot)

	if err := xproto.SetSelectionOwnerChecked(x.conn, x.window, x.atoms.clipboard, ownerTime).Check(); err != nil {
		return nil, fmt.Errorf("claim X11 clipboard: %w", err)
	}
	reply, err := xproto.GetSelectionOwner(x.conn, x.atoms.clipboard).Reply()
	if err != nil {
		return nil, fmt.Errorf("verify X11 clipboard ownership: %w", err)
	}
	if reply.Owner != x.window {
		return nil, errors.New("could not acquire X11 clipboard ownership")
	}

	go owner.run()
	return owner, nil
}

func (x *xClipboardConnection) serverTimestamp() (xproto.Timestamp, error) {
	data := []byte{1}
	if err := xproto.ChangePropertyChecked(
		x.conn,
		xproto.PropModeReplace,
		x.window,
		x.atoms.timestampProperty,
		x.atoms.timestampProperty,
		8,
		uint32(len(data)),
		data,
	).Check(); err != nil {
		return 0, fmt.Errorf("request X11 server timestamp: %w", err)
	}
	for {
		event, err := x.waitEvent(clipboardRequestTimeout)
		if err != nil {
			return 0, fmt.Errorf("get X11 server timestamp: %w", err)
		}
		property, ok := event.(xproto.PropertyNotifyEvent)
		if !ok || property.Window != x.window || property.Atom != x.atoms.timestampProperty || property.State != xproto.PropertyNewValue {
			continue
		}
		_ = xproto.DeletePropertyChecked(x.conn, x.window, x.atoms.timestampProperty).Check()
		return property.Time, nil
	}
}

func setActiveClipboardOwner(owner *clipboardOwner) {
	activeClipboardOwner.Lock()
	activeClipboardOwner.owner = owner
	activeClipboardOwner.Unlock()
}

func clearActiveClipboardOwner(owner *clipboardOwner) {
	activeClipboardOwner.Lock()
	if activeClipboardOwner.owner == owner {
		activeClipboardOwner.owner = nil
	}
	activeClipboardOwner.Unlock()
}

func (o *clipboardOwner) run() {
	defer func() {
		clearActiveClipboardOwner(o)
		o.close()
		close(o.done)
	}()

	for {
		event, xerr := o.conn.WaitForEvent()
		if xerr != nil || event == nil {
			return
		}

		switch event := event.(type) {
		case xproto.SelectionRequestEvent:
			if event.Owner == o.window && event.Selection == o.atoms.clipboard {
				o.handleSelectionRequest(event)
			}
		case xproto.PropertyNotifyEvent:
			o.handlePropertyNotify(event)
		case xproto.SelectionNotifyEvent:
			if event.Requestor == o.window && event.Target == o.atoms.saveTargets {
				o.finishPersistRequest()
			}
		case xproto.SelectionClearEvent:
			if event.Owner == o.window && event.Selection == o.atoms.clipboard {
				o.markLost()
			}
		}

		if o.shouldStop() {
			return
		}
	}
}

func (o *clipboardOwner) setPayloadsLocked(snapshot clipboardSnapshot) {
	o.payloads = make(map[xproto.Atom]clipboardPayload, len(snapshot.Payloads))
	o.order = make([]xproto.Atom, 0, len(snapshot.Payloads))
	for _, payload := range snapshot.Payloads {
		if _, exists := o.payloads[payload.Target]; !exists {
			o.order = append(o.order, payload.Target)
		}
		payload.Data = append([]byte(nil), payload.Data...)
		o.payloads[payload.Target] = payload
	}
}

func (o *clipboardOwner) markLost() {
	o.mu.Lock()
	o.owned = false
	o.mu.Unlock()
	o.lostOnce.Do(func() { close(o.lost) })
}

func (o *clipboardOwner) shouldStop() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return !o.owned && len(o.transfers) == 0 && o.persistCh == nil
}

func (o *clipboardOwner) handleSelectionRequest(request xproto.SelectionRequestEvent) {
	property := request.Property
	if property == xproto.AtomNone {
		property = request.Target
	}
	servedData := false

	if request.Target == o.atoms.multiple {
		var err error
		if request.Property == xproto.AtomNone {
			err = errors.New("MULTIPLE request has no property")
		} else {
			servedData, err = o.handleMultipleRequest(request.Requestor, request.Property)
		}
		if err != nil {
			property = xproto.AtomNone
		}
	} else {
		var supported bool
		var err error
		servedData, supported, err = o.writeTarget(request.Requestor, property, request.Target)
		if err != nil || !supported {
			property = xproto.AtomNone
		}
	}

	notify := xproto.SelectionNotifyEvent{
		Time:      request.Time,
		Requestor: request.Requestor,
		Selection: request.Selection,
		Target:    request.Target,
		Property:  property,
	}
	if err := xproto.SendEventChecked(o.conn, false, request.Requestor, 0, string(notify.Bytes())).Check(); err == nil && property != xproto.AtomNone && servedData {
		o.markServed()
	}
}

func (o *clipboardOwner) markServed() {
	o.mu.Lock()
	o.servedSequence++
	close(o.served)
	o.served = make(chan struct{})
	o.mu.Unlock()
}

func (o *clipboardOwner) writeTarget(requestor xproto.Window, property, target xproto.Atom) (bool, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	switch target {
	case o.atoms.targets:
		targets := make([]xproto.Atom, 0, len(o.order)+4)
		targets = append(targets, o.atoms.targets, o.atoms.multiple, o.atoms.timestamp, o.atoms.saveTargets)
		targets = append(targets, o.order...)
		data := encodeAtoms(targets)
		err := changeProperty(o.conn, requestor, property, xproto.AtomAtom, 32, data)
		return false, err == nil, err
	case o.atoms.timestamp:
		data := make([]byte, 4)
		xgb.Put32(data, uint32(o.ownerTime))
		err := changeProperty(o.conn, requestor, property, xproto.AtomInteger, 32, data)
		return false, err == nil, err
	case o.atoms.saveTargets:
		err := changeProperty(o.conn, requestor, property, o.atoms.null, 32, nil)
		return false, err == nil, err
	case o.atoms.multiple:
		return false, false, nil
	}

	payload, ok := o.payloads[target]
	if !ok {
		return false, false, nil
	}
	if len(payload.Data) <= o.maxPayload {
		if err := changeProperty(o.conn, requestor, property, payload.Type, payload.Format, payload.Data); err != nil {
			return false, false, err
		}
		return true, true, nil
	}

	if err := xproto.ChangeWindowAttributesChecked(
		o.conn,
		requestor,
		xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange},
	).Check(); err != nil {
		return false, false, fmt.Errorf("select incremental-transfer events: %w", err)
	}
	size := make([]byte, 4)
	xgb.Put32(size, uint32(len(payload.Data)))
	if err := changeProperty(o.conn, requestor, property, o.atoms.incr, 32, size); err != nil {
		return false, false, err
	}
	key := incrementalTransferKey{window: requestor, property: property}
	transfer := &incrementalTransfer{payload: payload}
	o.transfers[key] = transfer
	go o.expireIncrementalTransfer(key, transfer)
	return true, true, nil
}

func changeProperty(conn *xgb.Conn, window xproto.Window, property, typ xproto.Atom, format byte, data []byte) error {
	unit := int(format / 8)
	if unit == 0 || len(data)%unit != 0 {
		return fmt.Errorf("property data length %d is invalid for format %d", len(data), format)
	}
	return xproto.ChangePropertyChecked(
		conn,
		xproto.PropModeReplace,
		window,
		property,
		typ,
		format,
		uint32(len(data)/unit),
		data,
	).Check()
}

func (o *clipboardOwner) handleMultipleRequest(requestor xproto.Window, property xproto.Atom) (bool, error) {
	reply, err := xproto.GetProperty(
		o.conn,
		false,
		requestor,
		property,
		o.atoms.atomPair,
		0,
		uint32(maxClipboardSnapshotBytes/4+1),
	).Reply()
	if err != nil {
		return false, fmt.Errorf("read MULTIPLE request: %w", err)
	}
	if reply == nil || reply.Type != o.atoms.atomPair || reply.Format != 32 || reply.BytesAfter != 0 || len(reply.Value)%8 != 0 {
		return false, errors.New("invalid MULTIPLE request property")
	}

	pairs := append([]byte(nil), reply.Value...)
	servedData := false
	for offset := 0; offset < len(pairs); offset += 8 {
		target := xproto.Atom(xgb.Get32(pairs[offset:]))
		destination := xproto.Atom(xgb.Get32(pairs[offset+4:]))
		if destination == xproto.AtomNone || target == o.atoms.multiple {
			xgb.Put32(pairs[offset+4:], uint32(xproto.AtomNone))
			continue
		}
		served, supported, err := o.writeTarget(requestor, destination, target)
		if err != nil || !supported {
			xgb.Put32(pairs[offset+4:], uint32(xproto.AtomNone))
			continue
		}
		servedData = servedData || served
	}
	if err := changeProperty(o.conn, requestor, property, o.atoms.atomPair, 32, pairs); err != nil {
		return false, err
	}
	return servedData, nil
}

func (o *clipboardOwner) handlePropertyNotify(event xproto.PropertyNotifyEvent) {
	if event.State != xproto.PropertyDelete {
		return
	}
	key := incrementalTransferKey{window: event.Window, property: event.Atom}

	o.mu.Lock()
	defer o.mu.Unlock()
	transfer, ok := o.transfers[key]
	if !ok {
		return
	}

	if transfer.offset >= len(transfer.payload.Data) {
		_ = changeProperty(o.conn, key.window, key.property, transfer.payload.Type, transfer.payload.Format, nil)
		delete(o.transfers, key)
		return
	}

	unit := int(transfer.payload.Format / 8)
	chunkSize := o.maxPayload - o.maxPayload%unit
	end := transfer.offset + chunkSize
	if end > len(transfer.payload.Data) {
		end = len(transfer.payload.Data)
	}
	chunk := transfer.payload.Data[transfer.offset:end]
	if err := changeProperty(o.conn, key.window, key.property, transfer.payload.Type, transfer.payload.Format, chunk); err != nil {
		delete(o.transfers, key)
		return
	}
	transfer.offset = end
}

func (o *clipboardOwner) expireIncrementalTransfer(key incrementalTransferKey, transfer *incrementalTransfer) {
	timer := time.NewTimer(clipboardTransferTimeout)
	defer timer.Stop()
	<-timer.C

	o.mu.Lock()
	if current := o.transfers[key]; current == transfer {
		delete(o.transfers, key)
	}
	shouldClose := !o.owned && len(o.transfers) == 0 && o.persistCh == nil
	o.mu.Unlock()
	if shouldClose {
		o.close()
	}
}

// restore switches the live owner back to the captured representations. It
// never reclaims ownership: if another application copied something in the
// meantime, owned is already false (or a SelectionClear is queued) and the
// user's newer clipboard remains untouched.
func (o *clipboardOwner) restore(snapshot clipboardSnapshot) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.owned {
		return nil
	}
	if snapshot.HadOwner {
		o.setPayloadsLocked(snapshot.clone())
		return nil
	}

	// Reuse the acquisition timestamp. If another owner won a race but its
	// SelectionClear has not been processed yet, the old timestamp prevents
	// this request from clearing the newer selection.
	if err := xproto.SetSelectionOwnerChecked(
		o.conn,
		xproto.WindowNone,
		o.atoms.clipboard,
		o.ownerTime,
	).Check(); err != nil {
		return fmt.Errorf("restore empty clipboard: %w", err)
	}
	o.owned = false
	o.lostOnce.Do(func() { close(o.lost) })
	return nil
}

// beginPaste returns a marker that distinguishes clipboard reads caused by the
// upcoming paste from eager reads made after the selection was first published.
func (o *clipboardOwner) beginPaste() uint64 {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.servedSequence
}

func (o *clipboardOwner) waitUntilServedAfter(sequence uint64, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		o.mu.RLock()
		if o.servedSequence != sequence {
			o.mu.RUnlock()
			return
		}
		served := o.served
		o.mu.RUnlock()

		select {
		case <-served:
			// Recheck the sequence under the lock. markServed replaces the
			// channel for each successful payload request.
		case <-o.lost:
			return
		case <-o.done:
			return
		case <-timer.C:
			return
		}
	}
}

func (o *clipboardOwner) finishPersistRequest() {
	o.mu.Lock()
	ch := o.persistCh
	o.persistCh = nil
	o.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (o *clipboardOwner) persist(timeout time.Duration) error {
	o.mu.Lock()
	if !o.owned {
		o.mu.Unlock()
		return nil
	}
	if o.persistCh != nil {
		o.mu.Unlock()
		return errors.New("clipboard persistence already in progress")
	}
	manager, err := xproto.GetSelectionOwner(o.conn, o.atoms.clipboardManager).Reply()
	if err != nil {
		o.mu.Unlock()
		return fmt.Errorf("find X11 clipboard manager: %w", err)
	}
	if manager.Owner == xproto.WindowNone {
		o.mu.Unlock()
		return errClipboardManagerUnavailable
	}

	ch := make(chan struct{})
	o.persistCh = ch
	if err := xproto.ConvertSelectionChecked(
		o.conn,
		o.window,
		o.atoms.clipboardManager,
		o.atoms.saveTargets,
		xproto.AtomNone,
		o.ownerTime,
	).Check(); err != nil {
		o.persistCh = nil
		o.mu.Unlock()
		return fmt.Errorf("ask clipboard manager to persist selection: %w", err)
	}
	o.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return nil
	case <-o.done:
		return nil
	case <-timer.C:
		o.mu.Lock()
		if o.persistCh == ch {
			o.persistCh = nil
		}
		o.mu.Unlock()
		return errors.New("timed out while persisting X11 clipboard")
	}
}

type clipboardOwnerWire struct {
	Selection uint32
	OwnerTime uint32
	Snapshot  clipboardSnapshot
}

func (o *clipboardOwner) handoffToHelper() error {
	o.mu.RLock()
	if !o.owned {
		o.mu.RUnlock()
		return nil
	}
	wire := clipboardOwnerWire{
		Selection: uint32(o.atoms.clipboard),
		OwnerTime: uint32(o.ownerTime),
		Snapshot: clipboardSnapshot{
			HadOwner: true,
			Payloads: make([]clipboardPayload, 0, len(o.order)),
		},
	}
	for _, target := range o.order {
		payload, ok := o.payloads[target]
		if !ok {
			continue
		}
		payload.Data = append([]byte(nil), payload.Data...)
		wire.Snapshot.Payloads = append(wire.Snapshot.Payloads, payload)
	}
	o.mu.RUnlock()

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate clipboard owner executable: %w", err)
	}
	cmd := exec.Command(executable, "--clipboard-owner-helper")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open clipboard helper input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open clipboard helper output: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start clipboard helper: %w", err)
	}

	if err := gob.NewEncoder(stdin).Encode(wire); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("send clipboard snapshot to helper: %w", err)
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("finish clipboard helper input: %w", err)
	}

	type readyResult struct {
		line string
		err  error
	}
	ready := make(chan readyResult, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		ready <- readyResult{line: strings.TrimSpace(line), err: err}
	}()

	timer := time.NewTimer(clipboardPersistTimeout)
	defer timer.Stop()
	select {
	case result := <-ready:
		_ = stdout.Close()
		if result.err != nil || result.line != "ready" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			if result.err != nil {
				return fmt.Errorf("clipboard helper did not become ready: %w", result.err)
			}
			return fmt.Errorf("clipboard helper did not become ready: %s", result.line)
		}
		if err := cmd.Process.Release(); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("detach clipboard helper: %w", err)
		}
		return nil
	case <-timer.C:
		_ = stdout.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("timed out while starting clipboard helper")
	}
}

func validateClipboardSnapshot(snapshot clipboardSnapshot) error {
	if !snapshot.HadOwner {
		return errors.New("clipboard helper snapshot has no owner")
	}
	if len(snapshot.Payloads) > maxClipboardTargets {
		return fmt.Errorf("clipboard helper snapshot has too many targets: %d", len(snapshot.Payloads))
	}
	total := 0
	seen := make(map[xproto.Atom]struct{}, len(snapshot.Payloads))
	for _, payload := range snapshot.Payloads {
		if payload.Target == xproto.AtomNone {
			return errors.New("clipboard helper snapshot contains a None target")
		}
		if _, duplicate := seen[payload.Target]; duplicate {
			return fmt.Errorf("clipboard helper snapshot contains duplicate target %d", payload.Target)
		}
		seen[payload.Target] = struct{}{}
		if err := validatePayload(payload); err != nil {
			return fmt.Errorf("clipboard helper target %d: %w", payload.Target, err)
		}
		total += len(payload.Data)
		if total > maxClipboardSnapshotBytes {
			return fmt.Errorf("clipboard helper snapshot exceeds %d MiB", maxClipboardSnapshotBytes>>20)
		}
	}
	return nil
}

// ServeClipboardOwner decodes a snapshot from reader, claims CLIPBOARD using
// the original owner's timestamp and serves it until another application
// copies something. It is used only by the process's hidden helper mode.
func ServeClipboardOwner(reader io.Reader, ready io.Writer) error {
	var wire clipboardOwnerWire
	if err := gob.NewDecoder(io.LimitReader(reader, 2*maxClipboardSnapshotBytes)).Decode(&wire); err != nil {
		return fmt.Errorf("decode clipboard helper snapshot: %w", err)
	}
	if wire.OwnerTime == 0 {
		return errors.New("clipboard helper snapshot has no ownership timestamp")
	}
	if wire.Selection == 0 {
		return errors.New("clipboard helper snapshot has no selection atom")
	}
	if err := validateClipboardSnapshot(wire.Snapshot); err != nil {
		return err
	}

	x, err := newXClipboardConnection()
	if err != nil {
		return err
	}
	x.atoms.clipboard = xproto.Atom(wire.Selection)
	owner, err := startClipboardOwnerWithSnapshot(x, wire.Snapshot, xproto.Timestamp(wire.OwnerTime))
	if err != nil {
		x.close()
		return err
	}
	if _, err := fmt.Fprintln(ready, "ready"); err != nil {
		_ = owner.restore(clipboardSnapshot{HadOwner: false})
		return fmt.Errorf("signal clipboard helper readiness: %w", err)
	}
	<-owner.done
	return nil
}

// PersistClipboard asks the desktop clipboard manager to retain the current
// selection before this process exits. When no manager is present it hands
// ownership to a detached instance of this binary, preserving the detached
// owner behavior without an external clipboard executable.
func PersistClipboard() error {
	activeClipboardOwner.Lock()
	owner := activeClipboardOwner.owner
	activeClipboardOwner.Unlock()
	if owner == nil {
		return nil
	}
	persistErr := owner.persist(clipboardPersistTimeout)
	if persistErr == nil {
		return nil
	}
	if handoffErr := owner.handoffToHelper(); handoffErr != nil {
		return errors.Join(persistErr, handoffErr)
	}
	return nil
}
