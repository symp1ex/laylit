package windowslayouts

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	sOK                         = 0
	eNoInterface                = 0x80004002
	ePointer                    = 0x80004003
	tfProfileTypeKeyboardLayout = 0x0002
	tfIPSinkFlagActive          = 0x0001
	tfInvalidCookie             = 0xFFFFFFFF
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")

	clsidTFThreadMgr         = windows.GUID{Data1: 0x529A9E6B, Data2: 0x6587, Data3: 0x4F23, Data4: [8]byte{0xAB, 0x9E, 0x9C, 0x7D, 0x68, 0x3E, 0x3C, 0x50}}
	iidIUnknown              = windows.GUID{Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidITfThreadMgr          = windows.GUID{Data1: 0xAA80E801, Data2: 0x2021, Data3: 0x11D2, Data4: [8]byte{0x93, 0xE0, 0, 0x60, 0xB0, 0x67, 0xB8, 0x6E}}
	iidITfSource             = windows.GUID{Data1: 0x4EA48A35, Data2: 0x60AE, Data3: 0x446F, Data4: [8]byte{0x8F, 0xD6, 0xE6, 0xA8, 0xD8, 0x24, 0x59, 0xF7}}
	iidProfileActivationSink = windows.GUID{Data1: 0x71C6E74E, Data2: 0x0F28, Data3: 0x11D8, Data4: [8]byte{0xA8, 0x2A, 0, 0x06, 0x5B, 0x84, 0x43, 0x5C}}

	profileActivationVTable = profileActivationSinkVTable{
		QueryInterface: syscall.NewCallback(profileActivationQueryInterface),
		AddRef:         syscall.NewCallback(profileActivationAddRef),
		Release:        syscall.NewCallback(profileActivationRelease),
		OnActivated:    syscall.NewCallback(profileActivationOnActivated),
	}
	orphanedTSFSinks sync.Map
)

type tsfListener struct {
	threadMgr      *tfThreadMgr
	source         *tfSource
	sink           *profileActivationSink
	profileCookie  uint32
	activated      bool
	comInitialized bool
}

func startTSFListener(activated func(profileType, flags uint32, hkl uintptr)) (*tsfListener, error) {
	listener := &tsfListener{profileCookie: tfInvalidCookie}
	hresult, _, _ := procCoInitializeEx.Call(0, windows.COINIT_APARTMENTTHREADED)
	if err := hresultError("CoInitializeEx(COINIT_APARTMENTTHREADED)", hresult); err != nil {
		return nil, err
	}
	listener.comInitialized = true

	hresult, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidTFThreadMgr)), 0, windows.CLSCTX_INPROC_SERVER,
		uintptr(unsafe.Pointer(&iidITfThreadMgr)), uintptr(unsafe.Pointer(&listener.threadMgr)),
	)
	if err := hresultError("CoCreateInstance(CLSID_TF_ThreadMgr)", hresult); err != nil {
		_ = listener.close()
		return nil, err
	}

	var clientID uint32
	hresult = callCOM(listener.threadMgr.vtable.Activate, uintptr(unsafe.Pointer(listener.threadMgr)), uintptr(unsafe.Pointer(&clientID)))
	if err := hresultError("ITfThreadMgr.Activate", hresult); err != nil {
		_ = listener.close()
		return nil, err
	}
	listener.activated = true

	hresult = callCOM(
		listener.threadMgr.vtable.QueryInterface, uintptr(unsafe.Pointer(listener.threadMgr)),
		uintptr(unsafe.Pointer(&iidITfSource)), uintptr(unsafe.Pointer(&listener.source)),
	)
	if err := hresultError("ITfThreadMgr.QueryInterface(IID_ITfSource)", hresult); err != nil {
		_ = listener.close()
		return nil, err
	}

	listener.sink = newProfileActivationSink(activated)
	hresult = callCOM(
		listener.source.vtable.AdviseSink, uintptr(unsafe.Pointer(listener.source)),
		uintptr(unsafe.Pointer(&iidProfileActivationSink)), uintptr(unsafe.Pointer(listener.sink)),
		uintptr(unsafe.Pointer(&listener.profileCookie)),
	)
	runtime.KeepAlive(listener.sink)
	if err := hresultError("ITfSource.AdviseSink(IID_ITfInputProcessorProfileActivationSink)", hresult); err != nil {
		_ = listener.close()
		return nil, err
	}
	return listener, nil
}

func (listener *tsfListener) close() error {
	if listener == nil {
		return nil
	}
	if listener.sink != nil {
		listener.sink.active.Store(false)
	}

	var closeErr error
	unadvised := listener.profileCookie == tfInvalidCookie
	if listener.source != nil && !unadvised {
		hresult := callCOM(listener.source.vtable.UnadviseSink, uintptr(unsafe.Pointer(listener.source)), uintptr(listener.profileCookie))
		if err := hresultError("ITfSource.UnadviseSink(profile activation)", hresult); err != nil {
			closeErr = errors.Join(closeErr, err)
		} else {
			unadvised = true
			listener.profileCookie = tfInvalidCookie
		}
	}
	if listener.sink != nil {
		if unadvised {
			profileActivationRelease(unsafe.Pointer(listener.sink))
		} else {
			// A failed UnadviseSink cannot be made safe by freeing the Go-backed
			// callback object. Keep an inert reference for process lifetime.
			orphanedTSFSinks.Store(listener.sink, struct{}{})
		}
		listener.sink = nil
	}
	if listener.source != nil {
		callCOM(listener.source.vtable.Release, uintptr(unsafe.Pointer(listener.source)))
		listener.source = nil
	}
	if listener.threadMgr != nil && listener.activated {
		hresult := callCOM(listener.threadMgr.vtable.Deactivate, uintptr(unsafe.Pointer(listener.threadMgr)))
		closeErr = errors.Join(closeErr, hresultError("ITfThreadMgr.Deactivate", hresult))
		listener.activated = false
	}
	if listener.threadMgr != nil {
		callCOM(listener.threadMgr.vtable.Release, uintptr(unsafe.Pointer(listener.threadMgr)))
		listener.threadMgr = nil
	}
	if listener.comInitialized {
		procCoUninitialize.Call()
		listener.comInitialized = false
	}
	return closeErr
}

type tfThreadMgr struct{ vtable *tfThreadMgrVTable }

type tfThreadMgrVTable struct {
	QueryInterface        uintptr
	AddRef                uintptr
	Release               uintptr
	Activate              uintptr
	Deactivate            uintptr
	CreateDocumentMgr     uintptr
	EnumDocumentMgrs      uintptr
	GetFocus              uintptr
	SetFocus              uintptr
	AssociateFocus        uintptr
	IsThreadFocus         uintptr
	GetFunctionProvider   uintptr
	EnumFunctionProviders uintptr
	GetGlobalCompartment  uintptr
}

type tfSource struct{ vtable *tfSourceVTable }

type tfSourceVTable struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	AdviseSink     uintptr
	UnadviseSink   uintptr
}

type profileActivationSink struct {
	vtable    *profileActivationSinkVTable
	refs      atomic.Uint32
	active    atomic.Bool
	activated func(profileType, flags uint32, hkl uintptr)
}

type profileActivationSinkVTable struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	OnActivated    uintptr
}

func newProfileActivationSink(activated func(profileType, flags uint32, hkl uintptr)) *profileActivationSink {
	sink := &profileActivationSink{vtable: &profileActivationVTable, activated: activated}
	sink.refs.Store(1)
	sink.active.Store(true)
	return sink
}

func profileActivationQueryInterface(this unsafe.Pointer, interfaceID *windows.GUID, object *unsafe.Pointer) uintptr {
	if object == nil {
		return ePointer
	}
	*object = nil
	if interfaceID == nil {
		return eNoInterface
	}
	if *interfaceID != iidIUnknown && *interfaceID != iidProfileActivationSink {
		return eNoInterface
	}
	*object = this
	profileActivationAddRef(this)
	return sOK
}

func profileActivationAddRef(this unsafe.Pointer) uintptr {
	return uintptr((*profileActivationSink)(this).refs.Add(1))
}

func profileActivationRelease(this unsafe.Pointer) uintptr {
	sink := (*profileActivationSink)(this)
	for {
		current := sink.refs.Load()
		if current == 0 {
			return 0
		}
		if sink.refs.CompareAndSwap(current, current-1) {
			return uintptr(current - 1)
		}
	}
}

func profileActivationOnActivated(this unsafe.Pointer, profileType, _ uintptr, _, _, _ *windows.GUID, hkl, flags uintptr) uintptr {
	sink := (*profileActivationSink)(this)
	if sink.active.Load() && sink.activated != nil {
		sink.activated(uint32(profileType), uint32(flags), hkl)
	}
	return sOK
}

func callCOM(function uintptr, arguments ...uintptr) uintptr {
	result, _, _ := syscall.SyscallN(function, arguments...)
	return result
}

func hresultError(operation string, result uintptr) error {
	value := uint32(result)
	if int32(value) >= 0 {
		return nil
	}
	return fmt.Errorf("%s failed with HRESULT 0x%08X", operation, value)
}
