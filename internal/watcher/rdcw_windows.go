//go:build windows

package watcher

import (
	"encoding/binary"
	"errors"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const (
	bufferSize = 64000

	rdcwFilter = windows.FILE_NOTIFY_CHANGE_FILE_NAME |
		windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_ATTRIBUTES |
		windows.FILE_NOTIFY_CHANGE_SIZE |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE |
		windows.FILE_NOTIFY_CHANGE_SECURITY |
		windows.FILE_NOTIFY_CHANGE_LAST_ACCESS |
		windows.FILE_NOTIFY_CHANGE_CREATION
)

func parseRecords(buf []byte) []RawRecord {
	var records []RawRecord
	n := uint32(len(buf))
	offset := uint32(0)

	for offset < n {
		if offset+12 > n {
			break
		}
		nextOffset := binary.LittleEndian.Uint32(buf[offset : offset+4])
		action := binary.LittleEndian.Uint32(buf[offset+4 : offset+8])
		fileNameLen := binary.LittleEndian.Uint32(buf[offset+8 : offset+12])

		nameStart := offset + 12
		nameEnd := nameStart + fileNameLen
		if nameEnd > n {
			break
		}

		u16 := make([]uint16, fileNameLen/2)
		for i := range u16 {
			u16[i] = binary.LittleEndian.Uint16(buf[nameStart+uint32(i*2) : nameStart+uint32(i*2)+2])
		}
		name := string(utf16.Decode(u16))

		records = append(records, RawRecord{
			Action: Action(action),
			Name:   name,
		})

		if nextOffset == 0 {
			break
		}
		offset += nextOffset
	}

	return records
}

func openDirHandle(root string) (windows.Handle, error) {
	ptr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return windows.InvalidHandle, err
	}
	h, err := windows.CreateFile(
		ptr,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return h, nil
}

func (w *Watcher) emitDirDeletedRoot() {
	ev := Event{
		Type:  EventDeleted,
		Path:  w.root,
		IsDir: true,
	}
	w.handler.Handle(ev)
}

func (w *Watcher) readLoop() {
	defer func() {
		w.closeHandles()
		w.doneOnce.Do(func() {
			close(w.done)
		})
		close(w.goroutineDone)
	}()

	buf := make([]byte, bufferSize)
	// En el heap: el kernel escribe en él mientras la E/S está pendiente y el stack de la goroutine puede moverse.
	overlapped := new(windows.Overlapped)
	overlapped.HEvent = w.ovEvent

	for {
		waitRes, _ := windows.WaitForSingleObject(w.stopEvent, 0)
		if waitRes == windows.WAIT_OBJECT_0 {
			return
		}

		_ = windows.ResetEvent(w.ovEvent)
		overlapped.Internal = 0
		overlapped.InternalHigh = 0
		overlapped.Offset = 0
		overlapped.OffsetHigh = 0

		var bytesReturned uint32
		err := windows.ReadDirectoryChanges(
			w.handle,
			&buf[0],
			uint32(len(buf)),
			true,
			rdcwFilter,
			&bytesReturned,
			overlapped,
			0,
		)

		if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
			if errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
				return
			}
			w.emitDirDeletedRoot()
			return
		}

		handles := []windows.Handle{w.stopEvent, w.ovEvent}
		waitRes, waitErr := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if waitErr != nil || waitRes == windows.WAIT_OBJECT_0 {
			// Esperar a que la cancelación termine antes de cerrar el handle y soltar el buffer.
			var n uint32
			_ = windows.CancelIoEx(w.handle, overlapped)
			_ = windows.GetOverlappedResult(w.handle, overlapped, &n, true)
			return
		}

		if waitRes == windows.WAIT_OBJECT_0+1 {
			var bytesTransferred uint32
			err = windows.GetOverlappedResult(w.handle, overlapped, &bytesTransferred, false)
			if err != nil {
				if errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
					return
				}
				w.emitDirDeletedRoot()
				return
			}

			if bytesTransferred == 0 {
				continue
			}

			records := parseRecords(buf[:bytesTransferred])
			events := Translate(w.root, records)
			for _, ev := range events {
				w.handler.Handle(ev)
				if ev.Type == EventDeleted && ev.Path == w.root && ev.IsDir {
					return
				}
			}
		}
	}
}
