#include <libproc.h>
#include <sys/resource.h>
#include <time.h>
#include <stdio.h>
#include <stdlib.h>
#include <errno.h>
#include <unistd.h>
static unsigned long long ns(clockid_t id){struct timespec t;if(clock_gettime(id,&t))exit(2);return (unsigned long long)t.tv_sec*1000000000+t.tv_nsec;}
int main(int argc,char**argv){if(argc!=2)return 2;int pid=atoi(argv[1]);if(pid<=0)return 2;for(int i=0;i<4000;i++){struct rusage_info_v4 r;unsigned long long start=ns(CLOCK_MONOTONIC),utc=ns(CLOCK_REALTIME);int e=proc_pid_rusage(pid,RUSAGE_INFO_V4,(rusage_info_t*)&r);unsigned long long end=ns(CLOCK_MONOTONIC);if(e){printf("{\"available\":false,\"errno\":%d,\"pid\":%d,\"utcNanos\":%llu}\n",errno,pid,utc);fflush(stdout);return 0;}printf("{\"available\":true,\"pid\":%d,\"utcNanos\":%llu,\"startedNanos\":%llu,\"finishedNanos\":%llu,\"readBytes\":%llu,\"writeBytes\":%llu,\"userCpuNanos\":%llu,\"systemCpuNanos\":%llu}\n",pid,utc,start,end,r.ri_diskio_bytesread,r.ri_diskio_byteswritten,r.ri_user_time,r.ri_system_time);fflush(stdout);struct timespec wait={0,200000000};nanosleep(&wait,0);}return 3;}
