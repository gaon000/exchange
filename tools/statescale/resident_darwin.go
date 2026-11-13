//go:build darwin && cgo

package main

/*
#include <mach/mach.h>
#include <stdint.h>

static kern_return_t statescale_resident(uint64_t *bytes) {
    mach_task_basic_info_data_t info;
    mach_msg_type_number_t count = MACH_TASK_BASIC_INFO_COUNT;
    kern_return_t status = task_info(mach_task_self(), MACH_TASK_BASIC_INFO,
        (task_info_t)&info, &count);
    if (status != KERN_SUCCESS) return status;
    if (count != MACH_TASK_BASIC_INFO_COUNT) return KERN_FAILURE;
    *bytes = info.resident_size;
    return KERN_SUCCESS;
}
*/
import "C"

import "fmt"

// residentRSS reads this process's current resident pages, rather than the
// getrusage lifetime high-water. It does not sample physical footprint/swap.
func residentRSS() (int64, error) {
	var bytes C.uint64_t
	status := C.statescale_resident(&bytes)
	if status != C.KERN_SUCCESS || bytes == 0 || uint64(bytes) > 1<<63-1 {
		return 0, fmt.Errorf("current resident memory unavailable: Mach status %d", status)
	}
	return int64(bytes), nil
}
