// AMF Display Capture benchmark — uses real SDK C headers.
package main

/*
#cgo CFLAGS: -I${SRCDIR}/../../../vendor/amf/include
#cgo LDFLAGS: -ld3d11 -ldxgi -lole32

#include <stdlib.h>
#include <stdio.h>
#include <windows.h>
#include <d3d11.h>
#include <dxgi.h>
#include <initguid.h>

DEFINE_GUID(IID_IDXGIFactory1, 0x770aae78,0xf26f,0x4dba,0xa8,0x29,0x25,0x3c,0x83,0xd1,0xb3,0x87);

// Pull in the actual AMF C vtable definitions
#define AMF_CORE_STATIC

typedef long long int amf_int64;
typedef unsigned long long int amf_uint64;
typedef int amf_int32;
typedef unsigned int amf_uint;
typedef size_t amf_size;
typedef int amf_bool;
typedef long amf_long;
typedef void* amf_handle;
typedef int AMF_RESULT;
typedef int AMF_MEMORY_TYPE;
typedef int AMF_SURFACE_FORMAT;
typedef int AMF_DX_VERSION;
typedef int AMF_BUFFER_USAGE;
typedef int AMF_SURFACE_USAGE;
typedef int AMF_MEMORY_CPU_ACCESS;
typedef int AMF_AUDIO_FORMAT;

// Stub out types we don't use
typedef void AMFPropertyStorage;
typedef void AMFPropertyStorageObserver;
typedef void AMFBuffer;
typedef void AMFBufferObserver;
typedef void AMFSurface;
typedef void AMFSurfaceObserver;
typedef void AMFAudioBuffer;
typedef void AMFComputeFactory;
typedef void AMFComputeDevice;
typedef void AMFCompute;
typedef void AMFDebug;
typedef void AMFTrace;
typedef void AMFPrograms;
typedef void AMFCaps;
typedef void AMFPropertyInfo;
typedef void AMFDataAllocatorCB;
typedef void AMFComponentOptimizationCallback;

// AMFVariantStruct — simplified, enough to call SetProperty
typedef struct {
    amf_int64 type;
    union {
        amf_int64 int64Value;
        double doubleValue;
        amf_bool boolValue;
        void *pInterface;
    };
} AMFVariantStruct;

#define AMF_VARIANT_INT64 5
#define AMF_VARIANT_BOOL  2

static AMFVariantStruct makeInt64Var(amf_int64 val) {
    AMFVariantStruct v;
    v.type = AMF_VARIANT_INT64;
    v.int64Value = val;
    return v;
}

// Now define the actual vtable structs from the SDK headers
typedef struct AMFFactory AMFFactory;
typedef struct AMFContext AMFContext;
typedef struct AMFComponent AMFComponent;
typedef struct AMFData AMFData;

// From Factory.h
typedef struct AMFFactoryVtbl {
    AMF_RESULT (__stdcall *CreateContext)(AMFFactory *pThis, AMFContext **ppContext);
    AMF_RESULT (__stdcall *CreateComponent)(AMFFactory *pThis, AMFContext *pCtx, const wchar_t *id, AMFComponent **ppComp);
    AMF_RESULT (__stdcall *SetCacheFolder)(AMFFactory *pThis, const wchar_t *path);
    const wchar_t* (__stdcall *GetCacheFolder)(AMFFactory *pThis);
    AMF_RESULT (__stdcall *GetDebug)(AMFFactory *pThis, AMFDebug **ppDebug);
    AMF_RESULT (__stdcall *GetTrace)(AMFFactory *pThis, AMFTrace **ppTrace);
    AMF_RESULT (__stdcall *GetPrograms)(AMFFactory *pThis, AMFPrograms **ppPrograms);
} AMFFactoryVtbl;
struct AMFFactory { const AMFFactoryVtbl *pVtbl; };

// From Context.h — the REAL C vtable (AMFContext inherits AMFPropertyStorage, NOT PropertyStorageEx)
// Interface(3) + PropertyStorage(10) + Context methods
typedef struct AMFContextVtbl {
    // AMFInterface [0-2]
    amf_long (__stdcall *Acquire)(AMFContext *pThis);
    amf_long (__stdcall *Release)(AMFContext *pThis);
    AMF_RESULT (__stdcall *QueryInterface)(AMFContext *pThis, const void *iid, void **pp);
    // AMFPropertyStorage [3-12]
    AMF_RESULT (__stdcall *SetProperty)(AMFContext *pThis, const wchar_t *name, AMFVariantStruct value);
    AMF_RESULT (__stdcall *GetProperty)(AMFContext *pThis, const wchar_t *name, AMFVariantStruct *pValue);
    amf_bool   (__stdcall *HasProperty)(AMFContext *pThis, const wchar_t *name);
    amf_size   (__stdcall *GetPropertyCount)(AMFContext *pThis);
    AMF_RESULT (__stdcall *GetPropertyAt)(AMFContext *pThis, amf_size index, wchar_t *name, amf_size nameSize, AMFVariantStruct *pValue);
    AMF_RESULT (__stdcall *Clear)(AMFContext *pThis);
    AMF_RESULT (__stdcall *AddTo)(AMFContext *pThis, AMFPropertyStorage *pDest, amf_bool overwrite, amf_bool deep);
    AMF_RESULT (__stdcall *CopyTo)(AMFContext *pThis, AMFPropertyStorage *pDest, amf_bool deep);
    void       (__stdcall *AddObserver)(AMFContext *pThis, AMFPropertyStorageObserver *pObserver);
    void       (__stdcall *RemoveObserver)(AMFContext *pThis, AMFPropertyStorageObserver *pObserver);
    // AMFContext [13+]
    AMF_RESULT (__stdcall *Terminate)(AMFContext *pThis);                                        // 13
    AMF_RESULT (__stdcall *InitDX9)(AMFContext *pThis, void *pDX9Device);                        // 14
    void*      (__stdcall *GetDX9Device)(AMFContext *pThis, AMF_DX_VERSION dxVer);               // 15
    AMF_RESULT (__stdcall *LockDX9)(AMFContext *pThis);                                          // 16
    AMF_RESULT (__stdcall *UnlockDX9)(AMFContext *pThis);                                        // 17
    AMF_RESULT (__stdcall *InitDX11)(AMFContext *pThis, void *pDX11Device, AMF_DX_VERSION dxVer); // 18
    // ... rest omitted
} AMFContextVtbl;
struct AMFContext { const AMFContextVtbl *pVtbl; };

// From Component.h — the REAL C vtable
// Interface(3) + PropertyStorage(10) + PropertyStorageEx(4) + Component methods
typedef struct AMFComponentVtbl {
    // AMFInterface [0-2]
    amf_long   (__stdcall *Acquire)(AMFComponent *pThis);
    amf_long   (__stdcall *Release)(AMFComponent *pThis);
    AMF_RESULT (__stdcall *QueryInterface)(AMFComponent *pThis, const void *iid, void **pp);
    // AMFPropertyStorage [3-12]
    AMF_RESULT (__stdcall *SetProperty)(AMFComponent *pThis, const wchar_t *name, AMFVariantStruct value);
    AMF_RESULT (__stdcall *GetProperty)(AMFComponent *pThis, const wchar_t *name, AMFVariantStruct *pValue);
    amf_bool   (__stdcall *HasProperty)(AMFComponent *pThis, const wchar_t *name);
    amf_size   (__stdcall *GetPropertyCount)(AMFComponent *pThis);
    AMF_RESULT (__stdcall *GetPropertyAt)(AMFComponent *pThis, amf_size index, wchar_t *name, amf_size nameSize, AMFVariantStruct *pValue);
    AMF_RESULT (__stdcall *Clear)(AMFComponent *pThis);
    AMF_RESULT (__stdcall *AddTo)(AMFComponent *pThis, void *pDest, amf_bool overwrite, amf_bool deep);
    AMF_RESULT (__stdcall *CopyTo)(AMFComponent *pThis, void *pDest, amf_bool deep);
    void       (__stdcall *AddObserver)(AMFComponent *pThis, void *pObserver);
    void       (__stdcall *RemoveObserver)(AMFComponent *pThis, void *pObserver);
    // AMFPropertyStorageEx [13-16]
    amf_size   (__stdcall *GetPropertiesInfoCount)(AMFComponent *pThis);
    AMF_RESULT (__stdcall *GetPropertyInfoAt)(AMFComponent *pThis, amf_size index, const AMFPropertyInfo **ppInfo);
    AMF_RESULT (__stdcall *GetPropertyInfo)(AMFComponent *pThis, const wchar_t *name, const AMFPropertyInfo **ppInfo);
    AMF_RESULT (__stdcall *ValidateProperty)(AMFComponent *pThis, const wchar_t *name, AMFVariantStruct value, AMFVariantStruct *pOut);
    // AMFComponent [17-27]
    AMF_RESULT (__stdcall *Init)(AMFComponent *pThis, AMF_SURFACE_FORMAT fmt, amf_int32 w, amf_int32 h);  // 17
    AMF_RESULT (__stdcall *ReInit)(AMFComponent *pThis, amf_int32 w, amf_int32 h);                        // 18
    AMF_RESULT (__stdcall *Terminate)(AMFComponent *pThis);                                                // 19
    AMF_RESULT (__stdcall *Drain)(AMFComponent *pThis);                                                    // 20
    AMF_RESULT (__stdcall *Flush)(AMFComponent *pThis);                                                    // 21
    AMF_RESULT (__stdcall *SubmitInput)(AMFComponent *pThis, AMFData *pData);                              // 22
    AMF_RESULT (__stdcall *QueryOutput)(AMFComponent *pThis, AMFData **ppData);                            // 23
    void*      (__stdcall *GetContext)(AMFComponent *pThis);                                                // 24
    AMF_RESULT (__stdcall *SetOutputDataAllocatorCB)(AMFComponent *pThis, AMFDataAllocatorCB *cb);         // 25
    AMF_RESULT (__stdcall *GetCaps)(AMFComponent *pThis, AMFCaps **ppCaps);                                // 26
    AMF_RESULT (__stdcall *Optimize)(AMFComponent *pThis, AMFComponentOptimizationCallback *pCb);          // 27
} AMFComponentVtbl;
struct AMFComponent { const AMFComponentVtbl *pVtbl; };

// Globals
static HMODULE g_amfDLL = NULL;
static AMFFactory *g_factory = NULL;
static AMFContext *g_context = NULL;
static AMFComponent *g_capture = NULL;

typedef AMF_RESULT (__cdecl *AMFInit_Fn)(amf_uint64 version, AMFFactory **ppFactory);
typedef AMF_RESULT (__cdecl *AMFQueryVersion_Fn)(amf_uint64 *pVersion);

int bench_load(void) {
    g_amfDLL = LoadLibraryA("amfrt64.dll");
    if (!g_amfDLL) return -1;
    AMFInit_Fn fn = (AMFInit_Fn)GetProcAddress(g_amfDLL, "AMFInit");
    if (!fn) return -2;
    amf_uint64 ver = (amf_uint64)1<<48 | (amf_uint64)4<<32 | (amf_uint64)35<<16;
    AMF_RESULT r = fn(ver, &g_factory);
    return (r != 0 || !g_factory) ? -3 : 0;
}

amf_uint64 bench_version(void) {
    AMFQueryVersion_Fn fn = (AMFQueryVersion_Fn)GetProcAddress(g_amfDLL, "AMFQueryVersion");
    amf_uint64 v = 0; if (fn) fn(&v); return v;
}

int bench_create_context(void) {
    return (int)g_factory->pVtbl->CreateContext(g_factory, &g_context);
}

int bench_init_dx11(void) {
    IDXGIFactory1 *f = NULL;
    CreateDXGIFactory1(&IID_IDXGIFactory1, (void**)&f);
    if (!f) return -100;

    IDXGIAdapter *amd = NULL;
    for (UINT i = 0; i < 10; i++) {
        IDXGIAdapter *a = NULL;
        if (FAILED(f->lpVtbl->EnumAdapters(f, i, &a))) break;
        DXGI_ADAPTER_DESC d;
        a->lpVtbl->GetDesc(a, &d);
        fprintf(stderr, "  [%d] %ls (0x%04X)\n", i, d.Description, d.VendorId);
        if (d.VendorId == 0x1002 && !amd) amd = a; else a->lpVtbl->Release(a);
    }
    f->lpVtbl->Release(f);
    if (!amd) return -101;

    ID3D11Device *dev = NULL;
    ID3D11DeviceContext *ctx = NULL;
    D3D_FEATURE_LEVEL fl;
    HRESULT hr = D3D11CreateDevice((IDXGIAdapter*)amd, D3D_DRIVER_TYPE_UNKNOWN, NULL,
        0x20, NULL, 0, 7, &dev, &fl, &ctx);
    if (ctx) ctx->lpVtbl->Release(ctx);
    amd->lpVtbl->Release(amd);
    if (FAILED(hr)) return -102;

    // InitDX11 is at vtable offset 18 in AMFContextVtbl
    AMF_RESULT r = g_context->pVtbl->InitDX11(g_context, dev, 0);
    fprintf(stderr, "  InitDX11: %d\n", (int)r);
    return (int)r;
}

int bench_create_capture(void) {
    AMF_RESULT r = g_factory->pVtbl->CreateComponent(g_factory, g_context, L"AMFDisplayCapture", &g_capture);
    if (r != 0 || !g_capture) return (int)r ? (int)r : -200;

    // Set properties before Init
    // AMF_DISPLAYCAPTURE_MONITOR_INDEX = L"MonitorIndex" — amf_int64, default 0
    // AMF_DISPLAYCAPTURE_MODE = L"CaptureMode" — use GET_CURRENT_SURFACE (2) for polling
    g_capture->pVtbl->SetProperty(g_capture, L"CaptureMode", makeInt64Var(2)); // GET_CURRENT_SURFACE

    return 0;
}

int bench_capture_init(void) {
    // Init at vtable offset 17
    // For DisplayCapture, pass 0,0 to use monitor resolution
    AMF_RESULT r = g_capture->pVtbl->Init(g_capture, 0, 0, 0); // format=UNKNOWN(0), let it decide
    fprintf(stderr, "  Init(0,0,0): %d\n", (int)r);
    if (r != 0) {
        r = g_capture->pVtbl->Init(g_capture, 11, 0, 0); // BGRA
        fprintf(stderr, "  Init(BGRA,0,0): %d\n", (int)r);
    }
    if (r != 0) {
        r = g_capture->pVtbl->Init(g_capture, 11, 1920, 1080);
        fprintf(stderr, "  Init(BGRA,1920,1080): %d\n", (int)r);
    }
    return (int)r;
}

int bench_query(AMFData **ppData) {
    return (int)g_capture->pVtbl->QueryOutput(g_capture, ppData);
}

void bench_release_data(AMFData *p) {
    if (p) {
        // AMFInterface::Release at vtable offset 1
        typedef amf_long (__stdcall *Fn)(void*);
        void **vtbl = *(void***)p;
        ((Fn)vtbl[1])(p);
    }
}

void bench_cleanup(void) {
    if (g_capture) { g_capture->pVtbl->Terminate(g_capture); bench_release_data((AMFData*)g_capture); }
    if (g_context) { g_context->pVtbl->Terminate(g_context); bench_release_data((AMFData*)g_context); }
}
*/
import "C"

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"
	"unsafe"
)

func main() {
	nFrames := flag.Int("frames", 300, "frames to capture")
	flag.Parse()

	fmt.Println("=== AMF Display Capture Benchmark ===")

	if r := C.bench_load(); r != 0 { fmt.Fprintf(os.Stderr, "AMF load: %d\n", r); os.Exit(1) }
	v := uint64(C.bench_version())
	fmt.Printf("AMF %d.%d.%d\n", v>>48, (v>>32)&0xFFFF, (v>>16)&0xFFFF)

	if r := C.bench_create_context(); r != 0 { fmt.Fprintf(os.Stderr, "CreateContext: %d\n", r); os.Exit(1) }
	if r := C.bench_init_dx11(); r != 0 { fmt.Fprintf(os.Stderr, "InitDX11: %d\n", r); os.Exit(1) }
	fmt.Println("DX11 on AMD OK")

	if r := C.bench_create_capture(); r != 0 { fmt.Fprintf(os.Stderr, "CreateCapture: %d\n", r); os.Exit(1) }
	if r := C.bench_capture_init(); r != 0 { fmt.Fprintf(os.Stderr, "Init: %d\n", r); os.Exit(1) }
	fmt.Printf("Capturing %d frames...\n\n", *nFrames)
	defer C.bench_cleanup()

	// Warmup
	for i := 0; i < 10; i++ {
		var d *C.AMFData
		C.bench_query(&d)
		if d != nil { C.bench_release_data(d) }
		time.Sleep(16 * time.Millisecond)
	}

	// Benchmark
	lat := make([]float64, 0, *nFrames)
	reps, errs := 0, 0
	dl := time.Now().Add(30 * time.Second)
	for len(lat) < *nFrames && time.Now().Before(dl) {
		var d *C.AMFData
		t := time.Now()
		r := int(C.bench_query(&d))
		e := time.Since(t)
		if r == 0 && d != nil {
			lat = append(lat, float64(e.Microseconds())/1000.0)
			C.bench_release_data(d)
		} else if r == 1 {
			reps++
			time.Sleep(500 * time.Microsecond)
		} else {
			errs++
			if errs < 5 { fmt.Fprintf(os.Stderr, "QueryOutput: %d\n", r) }
			time.Sleep(time.Millisecond)
		}
	}

	if len(lat) == 0 {
		fmt.Fprintf(os.Stderr, "No frames (%d reps, %d errs)\n", reps, errs)
		os.Exit(1)
	}
	sort.Float64s(lat)
	n := len(lat)
	var s float64
	for _, l := range lat { s += l }

	fmt.Println("--- Results ---")
	fmt.Printf("Frames: %d (%d repeats, %d errors)\n", n, reps, errs)
	fmt.Printf("Min:  %.3f ms\n", lat[0])
	fmt.Printf("Avg:  %.3f ms\n", s/float64(n))
	fmt.Printf("P50:  %.3f ms\n", lat[n*50/100])
	fmt.Printf("P95:  %.3f ms\n", lat[n*95/100])
	fmt.Printf("P99:  %.3f ms\n", lat[n*99/100])
	fmt.Printf("Max:  %.3f ms\n", lat[n-1])
	fmt.Printf("FPS:  %.1f\n", 1000.0/(s/float64(n)))

	_ = unsafe.Pointer(nil)
}
