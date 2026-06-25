// Pipeline benchmark: AMF GPU capture+encode vs DXGI+OpenH264 CPU encode.
// Measures full capture-to-NAL latency per frame.
package main

/*
#cgo CFLAGS: -I${SRCDIR}/../../../vendor/amf/include
#cgo LDFLAGS: -ld3d11 -ldxgi -lole32

#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <windows.h>
#include <d3d11.h>
#include <dxgi.h>
#include <initguid.h>
// OpenH264 handled in separate binary

DEFINE_GUID(IID_IDXGIFactory1, 0x770aae78,0xf26f,0x4dba,0xa8,0x29,0x25,0x3c,0x83,0xd1,0xb3,0x87);

// ===== AMF types (same as capture_amf_cgo) =====
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

#define AMF_OK 0
#define AMF_REPEAT 1
#define AMF_EOF 23
#define AMF_SURFACE_NV12 1
#define AMF_SURFACE_BGRA 11
#define AMF_STD_CALL __stdcall
#define AMF_CDECL_CALL __cdecl

typedef void AMFPropertyStorage;
typedef void AMFPropertyStorageObserver;
typedef void AMFBuffer;
typedef void AMFSurface;
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
typedef void AMFBufferObserver;
typedef void AMFSurfaceObserver;
typedef int AMF_AUDIO_FORMAT;
typedef int AMF_BUFFER_USAGE;
typedef int AMF_SURFACE_USAGE;
typedef int AMF_MEMORY_CPU_ACCESS;

typedef struct { amf_int64 type; union { amf_int64 i; double d; amf_bool b; void *p; }; } AMFVariantStruct;
#define AMF_VARIANT_INT64 5
static AMFVariantStruct mkI64(amf_int64 v) { AMFVariantStruct s; s.type=AMF_VARIANT_INT64; s.i=v; return s; }

typedef struct AMFFactory AMFFactory;
typedef struct AMFContext AMFContext;
typedef struct AMFComponent AMFComponent;
typedef struct AMFData AMFData;

// Factory vtable
typedef struct { AMF_RESULT(__stdcall*CreateContext)(AMFFactory*,AMFContext**); AMF_RESULT(__stdcall*CreateComponent)(AMFFactory*,AMFContext*,const wchar_t*,AMFComponent**); } AMFFactoryVtbl;
struct AMFFactory { const AMFFactoryVtbl *pVtbl; };

// Context vtable: Interface(3)+PropertyStorage(10)+Context
typedef struct {
    amf_long(__stdcall*Acquire)(AMFContext*); amf_long(__stdcall*Release)(AMFContext*); AMF_RESULT(__stdcall*QI)(AMFContext*,const void*,void**);
    void*ps[10]; // PropertyStorage methods [3-12]
    AMF_RESULT(__stdcall*Terminate)(AMFContext*);                                    // 13
    AMF_RESULT(__stdcall*InitDX9)(AMFContext*,void*);                                // 14
    void*(__stdcall*GetDX9Device)(AMFContext*,int);                                  // 15
    AMF_RESULT(__stdcall*LockDX9)(AMFContext*);                                      // 16
    AMF_RESULT(__stdcall*UnlockDX9)(AMFContext*);                                    // 17
    AMF_RESULT(__stdcall*InitDX11)(AMFContext*,void*,int);                           // 18
} AMFContextVtbl;
struct AMFContext { const AMFContextVtbl *pVtbl; };

// Component vtable: Interface(3)+PropertyStorage(10)+PropertyStorageEx(4)+Component
typedef struct {
    amf_long(__stdcall*Acquire)(AMFComponent*); amf_long(__stdcall*Release)(AMFComponent*); AMF_RESULT(__stdcall*QI)(AMFComponent*,const void*,void**);
    AMF_RESULT(__stdcall*SetProperty)(AMFComponent*,const wchar_t*,AMFVariantStruct); void*ps[9]; // rest of PropertyStorage
    void*psx[4]; // PropertyStorageEx [13-16]
    AMF_RESULT(__stdcall*Init)(AMFComponent*,int,amf_int32,amf_int32);               // 17
    AMF_RESULT(__stdcall*ReInit)(AMFComponent*,amf_int32,amf_int32);                 // 18
    AMF_RESULT(__stdcall*Terminate)(AMFComponent*);                                   // 19
    AMF_RESULT(__stdcall*Drain)(AMFComponent*);                                       // 20
    AMF_RESULT(__stdcall*Flush)(AMFComponent*);                                       // 21
    AMF_RESULT(__stdcall*SubmitInput)(AMFComponent*,AMFData*);                        // 22
    AMF_RESULT(__stdcall*QueryOutput)(AMFComponent*,AMFData**);                       // 23
} AMFComponentVtbl;
struct AMFComponent { const AMFComponentVtbl *pVtbl; };

typedef AMF_RESULT(__cdecl*AMFInit_Fn)(amf_uint64,AMFFactory**);
typedef AMF_RESULT(__cdecl*AMFQueryVersion_Fn)(amf_uint64*);

static void amf_release(void *p) {
    if(p) { typedef amf_long(__stdcall*Fn)(void*); void**v=*(void***)p; ((Fn)v[1])(p); }
}

// ===== Pipeline A: AMF capture+encode =====
typedef struct {
    HMODULE dll;
    AMFFactory *factory;
    AMFContext *context;
    AMFComponent *capture;
    AMFComponent *encoder;
} AMFPipeline;

int amf_pipeline_init(AMFPipeline *p) {
    memset(p, 0, sizeof(*p));
    p->dll = LoadLibraryA("amfrt64.dll");
    if (!p->dll) return -1;
    AMFInit_Fn fn = (AMFInit_Fn)GetProcAddress(p->dll, "AMFInit");
    if (!fn) return -2;
    amf_uint64 ver = (amf_uint64)1<<48|(amf_uint64)4<<32|(amf_uint64)35<<16;
    if (fn(ver, &p->factory) != AMF_OK) return -3;
    if (p->factory->pVtbl->CreateContext(p->factory, &p->context) != AMF_OK) return -4;

    // Find AMD adapter and create D3D11 device
    IDXGIFactory1 *df = NULL;
    CreateDXGIFactory1(&IID_IDXGIFactory1, (void**)&df);
    IDXGIAdapter *amd = NULL;
    for (UINT i=0; i<10; i++) {
        IDXGIAdapter *a=NULL;
        if (FAILED(df->lpVtbl->EnumAdapters(df,i,&a))) break;
        DXGI_ADAPTER_DESC d; a->lpVtbl->GetDesc(a,&d);
        if (d.VendorId==0x1002 && !amd) amd=a; else a->lpVtbl->Release(a);
    }
    df->lpVtbl->Release(df);
    if (!amd) return -5;

    ID3D11Device *dev=NULL; ID3D11DeviceContext *ctx=NULL; D3D_FEATURE_LEVEL fl;
    D3D11CreateDevice((IDXGIAdapter*)amd,D3D_DRIVER_TYPE_UNKNOWN,NULL,0x20,NULL,0,7,&dev,&fl,&ctx);
    if (ctx) ctx->lpVtbl->Release(ctx);
    amd->lpVtbl->Release(amd);
    if (!dev) return -6;

    if (p->context->pVtbl->InitDX11(p->context, dev, 0) != AMF_OK) return -7;

    // Create capture
    if (p->factory->pVtbl->CreateComponent(p->factory, p->context, L"AMFDisplayCapture", &p->capture) != AMF_OK) return -8;
    p->capture->pVtbl->SetProperty(p->capture, L"CaptureMode", mkI64(2));
    if (p->capture->pVtbl->Init(p->capture, 0, 0, 0) != AMF_OK) return -9;

    // Create H.264 encoder
    if (p->factory->pVtbl->CreateComponent(p->factory, p->context, L"AMFVideoEncoderVCE_AVC", &p->encoder) != AMF_OK) return -10;
    p->encoder->pVtbl->SetProperty(p->encoder, L"Usage", mkI64(0));           // Transcoding
    p->encoder->pVtbl->SetProperty(p->encoder, L"Profile", mkI64(66));        // Baseline
    p->encoder->pVtbl->SetProperty(p->encoder, L"RateControlMethod", mkI64(0)); // CQP
    p->encoder->pVtbl->SetProperty(p->encoder, L"QP_I", mkI64(23));
    p->encoder->pVtbl->SetProperty(p->encoder, L"QP_P", mkI64(23));
    p->encoder->pVtbl->SetProperty(p->encoder, L"TargetBitrate", mkI64(5000000));
    p->encoder->pVtbl->SetProperty(p->encoder, L"FrameRate", mkI64(60));
    p->encoder->pVtbl->SetProperty(p->encoder, L"LowLatencyInternal", mkI64(1));
    // Init encoder at 1920x1080 BGRA (capture outputs BGRA; VCE accepts it directly)
    AMF_RESULT er = p->encoder->pVtbl->Init(p->encoder, AMF_SURFACE_BGRA, 1920, 1080);
    fprintf(stderr, "  AMF Encoder Init: %d\n", (int)er);
    if (er != AMF_OK) return -11;

    return 0;
}

// Capture one frame and encode it. Returns total time in microseconds, or negative on error.
// -1 = capture returned no frame (AMF_REPEAT), -2 = submit failed, -3 = no encoder output
long long amf_pipeline_frame(AMFPipeline *p) {
    LARGE_INTEGER freq, start, end;
    QueryPerformanceFrequency(&freq);

    // Wait for a capture frame (poll with short sleeps)
    AMFData *capData = NULL;
    for (int tries = 0; tries < 200; tries++) {
        AMF_RESULT r = p->capture->pVtbl->QueryOutput(p->capture, &capData);
        if (r == AMF_OK && capData) break;
        Sleep(1);
    }
    if (!capData) return -1;

    // Start timing from when we have the captured surface
    QueryPerformanceCounter(&start);

    // Submit captured surface to encoder (zero-copy, same AMFContext)
    AMF_RESULT r = p->encoder->pVtbl->SubmitInput(p->encoder, capData);
    amf_release(capData);
    if (r != AMF_OK && r != 6) { // AMF_INPUT_FULL
        fprintf(stderr, "  SubmitInput: %d\n", (int)r);
        return -2;
    }

    // Get encoded output - poll
    AMFData *encData = NULL;
    for (int i = 0; i < 500; i++) {
        r = p->encoder->pVtbl->QueryOutput(p->encoder, &encData);
        if (r == AMF_OK && encData) break;
        if (r == AMF_REPEAT) { Sleep(0); continue; }
        if (r != AMF_REPEAT) { fprintf(stderr, "  QO: %d\n", (int)r); break; }
    }

    QueryPerformanceCounter(&end);

    if (encData) amf_release(encData);
    if (!encData) return -3;

    return (long long)((end.QuadPart - start.QuadPart) * 1000000 / freq.QuadPart);
}

void amf_pipeline_cleanup(AMFPipeline *p) {
    if (p->encoder) { p->encoder->pVtbl->Terminate(p->encoder); amf_release(p->encoder); }
    if (p->capture) { p->capture->pVtbl->Terminate(p->capture); amf_release(p->capture); }
    if (p->context) { p->context->pVtbl->Terminate(p->context); amf_release(p->context); }
}

// Pipeline B (DXGI+OpenH264) will be benchmarked in a separate binary
*/
import "C"

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"
)

func main() {
	nFrames := flag.Int("frames", 300, "frames per pipeline")
	flag.Parse()

	fmt.Println("=== Pipeline Benchmark: AMF GPU vs DXGI+OpenH264 CPU ===")
	fmt.Println()

	// ===== Pipeline A: AMF capture → AMF VCE H.264 encode =====
	fmt.Println("[Pipeline A] AMF Capture → AMF VCE H.264 (all GPU)")
	var amfP C.AMFPipeline
	if r := C.amf_pipeline_init(&amfP); r != 0 {
		fmt.Fprintf(os.Stderr, "AMF pipeline init failed: %d\n", r)
	} else {
		defer C.amf_pipeline_cleanup(&amfP)

		// Warmup
		for i := 0; i < 10; i++ {
			C.amf_pipeline_frame(&amfP)
			time.Sleep(16 * time.Millisecond)
		}

		latencies := make([]float64, 0, *nFrames)
		captureMisses := 0
		submitErrors := 0
		encodeErrors := 0
		deadline := time.Now().Add(60 * time.Second)
		for len(latencies) < *nFrames && time.Now().Before(deadline) {
			us := int64(C.amf_pipeline_frame(&amfP))
			switch {
			case us == -1:
				captureMisses++
			case us == -2:
				submitErrors++
			case us == -3:
				encodeErrors++
			case us >= 0:
				latencies = append(latencies, float64(us)/1000.0)
			}
		}

		if len(latencies) > 0 {
			sort.Float64s(latencies)
			n := len(latencies)
			var sum float64
			for _, l := range latencies {
				sum += l
			}
			fmt.Printf("  Frames:  %d captured+encoded, %d cap misses, %d submit err, %d encode err\n", n, captureMisses, submitErrors, encodeErrors)
			fmt.Printf("  Min:     %.3f ms\n", latencies[0])
			fmt.Printf("  Avg:     %.3f ms\n", sum/float64(n))
			fmt.Printf("  P50:     %.3f ms\n", latencies[n*50/100])
			fmt.Printf("  P95:     %.3f ms\n", latencies[n*95/100])
			fmt.Printf("  P99:     %.3f ms\n", latencies[n*99/100])
			fmt.Printf("  Max:     %.3f ms\n", latencies[n-1])
			fmt.Printf("  FPS:     %.1f\n", 1000.0/(sum/float64(n)))
		} else {
			fmt.Println("  No frames captured+encoded")
		}
	}
	fmt.Println()

	fmt.Println("[Pipeline B] DXGI DD + OpenH264 CPU -- run separately via capture_dxgi + openh264 bench")
}
