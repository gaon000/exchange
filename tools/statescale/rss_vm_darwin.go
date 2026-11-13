//go:build darwin && cgo

package main

/*
#include <mach/mach.h>
#include <stdint.h>
typedef struct {
 uint64_t resident,footprint,reusable,compressed,internal,external,virtual_bytes;
} statescale_vm_memory;
static kern_return_t statescale_vm(statescale_vm_memory *out) {
 task_vm_info_data_t info;
 mach_msg_type_number_t count=TASK_VM_INFO_COUNT;
 kern_return_t status=task_info(mach_task_self(),TASK_VM_INFO,(task_info_t)&info,&count);
 if (status!=KERN_SUCCESS) return status;
 if (count<TASK_VM_INFO_REV1_COUNT) return KERN_FAILURE;
 out->resident=info.resident_size;out->footprint=info.phys_footprint;
 out->reusable=info.reusable;out->compressed=info.compressed;
 out->internal=info.internal;out->external=info.external;out->virtual_bytes=info.virtual_size;
 return KERN_SUCCESS;
}
*/
import "C"
import "fmt"

func vmMemory() (rssVM, error) {
	var v C.statescale_vm_memory
	if status := C.statescale_vm(&v); status != C.KERN_SUCCESS {
		return rssVM{}, fmt.Errorf("Mach VM memory unavailable: status %d", status)
	}
	return rssVM{ResidentBytes: uint64(v.resident), PhysicalFootprintBytes: uint64(v.footprint), ReusableBytes: uint64(v.reusable), CompressedBytes: uint64(v.compressed), InternalBytes: uint64(v.internal), ExternalBytes: uint64(v.external), VirtualBytes: uint64(v.virtual_bytes)}, nil
}
