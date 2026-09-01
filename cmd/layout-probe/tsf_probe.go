package main

import (
	"fmt"
	"log"
	"runtime"
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
	ole32                    = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance     = ole32.NewProc("CoCreateInstance")
	procCoInitializeEx       = ole32.NewProc("CoInitializeEx")
	procCoUninitialize       = ole32.NewProc("CoUninitialize")
	clsidTFThreadMgr         = windows.GUID{Data1: 0x529A9E6B, Data2: 0x6587, Data3: 0x4F23, Data4: [8]byte{0xAB, 0x9E, 0x9C, 0x7D, 0x68, 0x3E, 0x3C, 0x50}}
	iidIUnknown              = windows.GUID{Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidITfThreadMgr          = windows.GUID{Data1: 0xAA80E801, Data2: 0x2021, Data3: 0x11D2, Data4: [8]byte{0x93, 0xE0, 0, 0x60, 0xB0, 0x67, 0xB8, 0x6E}}
	iidITfSource             = windows.GUID{Data1: 0x4EA48A35, Data2: 0x60AE, Data3: 0x446F, Data4: [8]byte{0x8F, 0xD6, 0xE6, 0xA8, 0xD8, 0x24, 0x59, 0xF7}}
	iidProfileActivationSink = windows.GUID{Data1: 0x71C6E74E, Data2: 0x0F28, Data3: 0x11D8, Data4: [8]byte{0xA8, 0x2A, 0, 0x06, 0x5B, 0x84, 0x43, 0x5C}}
	iidActiveLanguageSink    = windows.GUID{Data1: 0xB246CB75, Data2: 0xA93E, Data3: 0x4652, Data4: [8]byte{0xBF, 0x8C, 0xB3, 0xFE, 0x0C, 0xFD, 0x7E, 0x57}}

	profileActivationVTable = profileActivationSinkVTable{
		QueryInterface: syscall.NewCallback(profileActivationQueryInterface),
		AddRef:         syscall.NewCallback(profileActivationAddRef),
		Release:        syscall.NewCallback(profileActivationRelease),
		OnActivated:    syscall.NewCallback(profileActivationOnActivated),
	}
	activeLanguageVTable = activeLanguageSinkVTable{
		QueryInterface: syscall.NewCallback(activeLanguageQueryInterface),
		AddRef:         syscall.NewCallback(activeLanguageAddRef),
		Release:        syscall.NewCallback(activeLanguageRelease),
		OnActivated:    syscall.NewCallback(activeLanguageOnActivated),
	}
)

type tsfProbe struct {
	logger             *log.Logger
	threadMgr          *tfThreadMgr
	source             *tfSource
	profileSink        *profileActivationSink
	activeLanguageSink *activeLanguageSink
	profileCookie      uint32
	activeCookie       uint32
	activated          bool
	comInitialized     bool
}

func startTSFProbe(logger *log.Logger) (*tsfProbe, error) {
	probe := &tsfProbe{logger: logger, profileCookie: tfInvalidCookie, activeCookie: tfInvalidCookie}

	hresult, _, _ := procCoInitializeEx.Call(0, windows.COINIT_APARTMENTTHREADED)
	if err := hresultError("CoInitializeEx(COINIT_APARTMENTTHREADED)", hresult); err != nil {
		return nil, err
	}
	probe.comInitialized = true

	var threadMgr *tfThreadMgr
	hresult, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidTFThreadMgr)),
		0,
		windows.CLSCTX_INPROC_SERVER,
		uintptr(unsafe.Pointer(&iidITfThreadMgr)),
		uintptr(unsafe.Pointer(&threadMgr)),
	)
	if err := hresultError("CoCreateInstance(CLSID_TF_ThreadMgr)", hresult); err != nil {
		probe.close()
		return nil, err
	}
	probe.threadMgr = threadMgr

	var clientID uint32
	hresult = callCOM(probe.threadMgr.vtable.Activate, uintptr(unsafe.Pointer(probe.threadMgr)), uintptr(unsafe.Pointer(&clientID)))
	if err := hresultError("ITfThreadMgr.Activate", hresult); err != nil {
		probe.close()
		return nil, err
	}
	probe.activated = true

	var source *tfSource
	hresult = callCOM(
		probe.threadMgr.vtable.QueryInterface,
		uintptr(unsafe.Pointer(probe.threadMgr)),
		uintptr(unsafe.Pointer(&iidITfSource)),
		uintptr(unsafe.Pointer(&source)),
	)
	if err := hresultError("ITfThreadMgr.QueryInterface(IID_ITfSource)", hresult); err != nil {
		probe.close()
		return nil, err
	}
	probe.source = source

	probe.profileSink = newProfileActivationSink(logger)
	hresult = callCOM(
		probe.source.vtable.AdviseSink,
		uintptr(unsafe.Pointer(probe.source)),
		uintptr(unsafe.Pointer(&iidProfileActivationSink)),
		uintptr(unsafe.Pointer(probe.profileSink)),
		uintptr(unsafe.Pointer(&probe.profileCookie)),
	)
	runtime.KeepAlive(probe.profileSink)
	if err := hresultError("ITfSource.AdviseSink(IID_ITfInputProcessorProfileActivationSink)", hresult); err != nil {
		probe.close()
		return nil, err
	}

	probe.activeLanguageSink = newActiveLanguageSink(logger)
	hresult = callCOM(
		probe.source.vtable.AdviseSink,
		uintptr(unsafe.Pointer(probe.source)),
		uintptr(unsafe.Pointer(&iidActiveLanguageSink)),
		uintptr(unsafe.Pointer(probe.activeLanguageSink)),
		uintptr(unsafe.Pointer(&probe.activeCookie)),
	)
	runtime.KeepAlive(probe.activeLanguageSink)
	if err := hresultError("ITfSource.AdviseSink(IID_ITfActiveLanguageProfileNotifySink)", hresult); err != nil {
		probe.close()
		return nil, err
	}

	logger.Printf(
		"timestamp=%s phase=tsf-startup result=registered apartment=STA client_id=%d profile_cookie=%d active_language_cookie=%d callback_owner_tid=%d",
		timestamp(), clientID, probe.profileCookie, probe.activeCookie, windows.GetCurrentThreadId(),
	)
	return probe, nil
}

func (probe *tsfProbe) close() {
	if probe.source != nil && probe.activeCookie != tfInvalidCookie {
		hresult := callCOM(probe.source.vtable.UnadviseSink, uintptr(unsafe.Pointer(probe.source)), uintptr(probe.activeCookie))
		probe.logger.Printf("timestamp=%s phase=tsf-shutdown operation=unadvise-active-language hresult=0x%08X", timestamp(), uint32(hresult))
		probe.activeCookie = tfInvalidCookie
	}
	if probe.source != nil && probe.profileCookie != tfInvalidCookie {
		hresult := callCOM(probe.source.vtable.UnadviseSink, uintptr(unsafe.Pointer(probe.source)), uintptr(probe.profileCookie))
		probe.logger.Printf("timestamp=%s phase=tsf-shutdown operation=unadvise-profile hresult=0x%08X", timestamp(), uint32(hresult))
		probe.profileCookie = tfInvalidCookie
	}
	if probe.activeLanguageSink != nil {
		activeLanguageRelease(unsafe.Pointer(probe.activeLanguageSink))
		probe.activeLanguageSink = nil
	}
	if probe.profileSink != nil {
		profileActivationRelease(unsafe.Pointer(probe.profileSink))
		probe.profileSink = nil
	}
	if probe.source != nil {
		callCOM(probe.source.vtable.Release, uintptr(unsafe.Pointer(probe.source)))
		probe.source = nil
	}
	if probe.threadMgr != nil && probe.activated {
		hresult := callCOM(probe.threadMgr.vtable.Deactivate, uintptr(unsafe.Pointer(probe.threadMgr)))
		probe.logger.Printf("timestamp=%s phase=tsf-shutdown operation=deactivate hresult=0x%08X", timestamp(), uint32(hresult))
		probe.activated = false
	}
	if probe.threadMgr != nil {
		callCOM(probe.threadMgr.vtable.Release, uintptr(unsafe.Pointer(probe.threadMgr)))
		probe.threadMgr = nil
	}
	if probe.comInitialized {
		procCoUninitialize.Call()
		probe.comInitialized = false
	}
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
	vtable *profileActivationSinkVTable
	refs   atomic.Uint32
	logger *log.Logger
}

type profileActivationSinkVTable struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	OnActivated    uintptr
}

func newProfileActivationSink(logger *log.Logger) *profileActivationSink {
	sink := &profileActivationSink{vtable: &profileActivationVTable, logger: logger}
	sink.refs.Store(1)
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

func profileActivationOnActivated(this unsafe.Pointer, profileType, languageID uintptr, classID, categoryID, profileID *windows.GUID, hkl, flags uintptr) uintptr {
	sink := (*profileActivationSink)(this)
	snapshot := foregroundSnapshot()
	result := "ignored"
	if uint32(profileType) == tfProfileTypeKeyboardLayout && uint32(flags)&tfIPSinkFlagActive != 0 && hkl != 0 {
		result = "candidate"
	}
	sink.logger.Printf(
		"timestamp=%s phase=tsf-profile-callback callback_tid=%d profile_type=%d language_id=0x%04X class_id=%s category_id=%s profile_id=%s hkl=0x%X flags=0x%X active=%t result=%s foreground_hwnd=0x%X foreground_pid=%d foreground_tid=%d foreground_hkl=0x%X matches_foreground=%t",
		timestamp(), windows.GetCurrentThreadId(), uint32(profileType), uint16(languageID), guidString(classID), guidString(categoryID), guidString(profileID), hkl, uint32(flags),
		uint32(flags)&tfIPSinkFlagActive != 0, result, snapshot.hwnd, snapshot.pid, snapshot.threadID, snapshot.hkl, hkl == snapshot.hkl,
	)
	return sOK
}

type activeLanguageSink struct {
	vtable *activeLanguageSinkVTable
	refs   atomic.Uint32
	logger *log.Logger
}

type activeLanguageSinkVTable struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	OnActivated    uintptr
}

func newActiveLanguageSink(logger *log.Logger) *activeLanguageSink {
	sink := &activeLanguageSink{vtable: &activeLanguageVTable, logger: logger}
	sink.refs.Store(1)
	return sink
}

func activeLanguageQueryInterface(this unsafe.Pointer, interfaceID *windows.GUID, object *unsafe.Pointer) uintptr {
	if object == nil {
		return ePointer
	}
	*object = nil
	if interfaceID == nil {
		return eNoInterface
	}
	if *interfaceID != iidIUnknown && *interfaceID != iidActiveLanguageSink {
		return eNoInterface
	}
	*object = this
	activeLanguageAddRef(this)
	return sOK
}

func activeLanguageAddRef(this unsafe.Pointer) uintptr {
	return uintptr((*activeLanguageSink)(this).refs.Add(1))
}

func activeLanguageRelease(this unsafe.Pointer) uintptr {
	sink := (*activeLanguageSink)(this)
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

func activeLanguageOnActivated(this unsafe.Pointer, classID, profileID *windows.GUID, activated uintptr) uintptr {
	sink := (*activeLanguageSink)(this)
	snapshot := foregroundSnapshot()
	sink.logger.Printf(
		"timestamp=%s phase=tsf-active-language-callback callback_tid=%d class_id=%s profile_id=%s activated=%t foreground_hwnd=0x%X foreground_pid=%d foreground_tid=%d foreground_hkl=0x%X",
		timestamp(), windows.GetCurrentThreadId(), guidString(classID), guidString(profileID), activated != 0,
		snapshot.hwnd, snapshot.pid, snapshot.threadID, snapshot.hkl,
	)
	return sOK
}

func guidString(pointer *windows.GUID) string {
	if pointer == nil {
		return "<nil>"
	}
	return pointer.String()
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
