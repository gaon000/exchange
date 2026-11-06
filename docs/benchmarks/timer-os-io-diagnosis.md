# 타이머 분리 재현과 저장 호출의 OS I/O 관찰

현재 저장소에서 다시 실행·검증·분석하는 명령은 [실험 도구 안내](../diagnostic-tools.md)를 따른다. 아래 경로·명령은 당시 측정 근거로 보존한다.

2026-11-03, [이전 trace 진단](trace-host-diagnosis.md)에 이어 타이머 단독·worker 전달만 각각 10분 스케줄, 실제 거래소의 snapshot 켬을 10분 실행했다. **HTTP·거래 엔진·WAL/snapshot 작업 없이 worker의 예정 대비 50ms 초과가 1건 재현됐다. 다만 추가 지연 대부분이 worker가 runnable로 바뀌기 전이었으며 원인은 미확정이다. 저장에서는 WAL F_FULLFSYNC 최대 39.639ms와 프로세스 I/O 누계를 관찰했지만, 관리자 권한이 필요한 커널 이벤트 추적은 확보하지 못했다.**

[기계 판독 근거](20261103-timer-os-io-report.json)에 전체 집계·선택 호출·시간 전이·원본 해시를 남긴다. 원본·도구·overlay·trace는 Git 제외 `var/timer-io-20261103.01lq38i1`에 보존한다.

## 실행과 권한 범위

기반 revision은 `74a1206af94ada42eabbc5440e651c779bf181e5`다. 기존 feature branch와 미커밋 문서를 보존했고 production source·dependency·저장 계약은 변경하지 않았다. 같은 macOS 15.8 arm64/APFS·M3 Pro 11 CPU/18GiB·Go 1.27.3에서 task-local 도구와 overlay를 빌드했다.

`sudo -n fs_usage`의 현재 셸 PID 한정 1초 probe는 `sudo: a password is required`로 실패했다. 현재 개발 도구 설정의 `xcrun xctrace list templates`도 사용할 수 없었다. 비밀번호 입력·SIP 변경·도구 설치는 하지 않았다. 따라서 **커널의 fcntl 이벤트·I/O 대기열·장치 완료 시각은 미확보**다. 이번에 수집한 OS 자료는 `proc_pid_rusage(RUSAGE_INFO_V4)`가 제공하는 대상 프로세스의 disk read/write bytes·CPU 누계이며 커널 이벤트 추적을 대체하지 않는다.

실험 순서는 타이머만→타이머와 worker→실제 저장 부하다. 앞 두 실행 뒤 각각 30초 유휴했다. 각 1회이고 다른 사용자 프로세스와 OS 캐시를 통제하지 않았다. 분석·테스트·압축은 부하와 겹치지 않았다. trace·관찰 도구 자체의 비용도 존재하므로 무진단 성능 개선율이나 최대 처리량으로 해석하지 않는다.

## 타이머 분리 실험

각 조건은 100회/초, 10ms 간격의 절대 예정 시각 60,000개다. 준비·warmup·HTTP·거래 엔진·WAL/snapshot 작업은 없다. trace 버퍼의 실행 중 파일 쓰기와 계측 비용은 존재한다. 기존 생성기와 같은 NewTimer/select 대기, 20ms 뒤 epoch, 전달 대기열 없는 handoff와 50ms 검사 구조를 사용한다. worker 조건은 2,048개 worker를 준비한 뒤 시계를 시작한다. worker는 도착 시각만 기록한다. trace와 표본 배열을 유지하고 표본 JSON은 종료 후 쓴다. 이는 별도의 재현 도구이며 거래소 요청 유효성/처리량 측정이 아니다.

| 지표 | 타이머만 | 타이머 + worker |
| --- | ---: | ---: |
| 예정 표본 | 60,000 | 60,000 |
| 타이머 종료 지연 p99 | 1.190ms | 1.179ms |
| 타이머 종료 지연 최대 | 29.987ms | 45.726ms |
| 타이머 종료 10ms 초과 | 12 | 7 |
| 타이머 종료 50ms 초과 | 0 | 0 |
| 전달 전 관찰→worker p99 | 해당 없음 | 0.066ms |
| 전달 전 관찰→worker 최대 | 해당 없음 | 30.603ms |
| worker 예정 대비 지연 최대 | 해당 없음 | 55.974ms |
| worker 50ms 한도 초과 | 해당 없음 | 1 |

단독 조건에서도 약 30ms 지연은 나타났다. 이 표본 직전의 select 대기가 길었고, runnable 대기만으로 전체 지연을 설명할 수 없다. Go runtime·OS 타이머·스레드 스케줄링 중 어느 원인인지는 확정하지 않았다.

worker 조건의 index 4093은 예정 시각 40.930초, 타이머 종료 40.955370208초, worker 첫 실행 40.985973583초였다. 타이머 종료 지연 25.370208ms에 이후 30.603375ms가 더해져 55.973583ms가 됐다. 기존 생성기에서는 worker 검사에서 거절될 형태지만, 이 실험은 실제 주문을 보내지 않았으므로 실제 주문 미전송 1건으로 세지 않는다.

trace에서 scheduler Log 이후 실제 채널 전달로 worker가 runnable로 바뀌기까지 약 30.184ms가 지났고, worker의 Runnable→Running은 0.250368ms였다. **전달 전 관찰→worker 시간 전체를 worker 실행 대기로 부르면 잘못된 해석이다.** 그 사이 scheduler goroutine의 Go 상태는 Running으로 기록됐다. OS 스레드가 실제 CPU를 계속 사용했는지, trace 기록·runtime·OS 정지 중 무엇이 지연을 만들었는지는 알 수 없다. 전달 전 Log도 해당 경로에 있으므로 계측 비용이 원인에 포함됐을 가능성을 배제하지 않는다. 무진단 대조 실행은 하지 않았다.

두 trace의 UTC−trace 경과 끝점 차이는 약 -5.994/-7.584ms였다. 예정/대기/worker 표본과 trace 표시는 index·monotonic offset·delay가 모두 일치했다. 분석에는 같은 프로세스의 monotonic 차이를 사용했고 끝점 차이를 전체 오차 상한으로 쓰지 않았다.

## 실제 저장 호출과 OS 누계

기존 config-v1/soak-10m-v1의 bytes와 seed 17, 100 requests/s, 준비 20건·warmup 5초·측정 600초, 동시 한도 2,048·deadline 5초·발사 지연 한도 50ms를 유지했다. snapshot 5초, 서버/생성기 계측 500ms이며 기존 trace overlay에 C 호출 관찰을 추가했다.

실제 fcntl(F_FULLFSYNC)와 EINTR 재시도 전체를 CLOCK_MONOTONIC 경과 시간과 CLOCK_THREAD_CPUTIME_ID로 감쌌다. errno는 clock 조회 전에 보존하고 기존 성공/오류 판단을 유지했다. record는 메모리에 제한해 보관하고 두 저장 worker가 drain된 뒤 파일로 쓴다. 프로세스 I/O sampler는 200ms 간격으로 별도 실행했다. 지원 실패는 available=false로 남겨 0과 구분했다.

| WAL 호출 지표 | 관찰값 |
| --- | ---: |
| 관찰한 WAL C 호출 | 50,928개 |
| C 호출 경과 시간 중앙값 / p99 | 2.855 / 8.794ms |
| C 호출 경과 시간 최대 | 39.639ms |
| 호출 스레드 CPU 중앙값 / p99 | 0.099 / 0.224ms |
| 가장 긴 WAL 호출의 스레드 CPU | 0.036ms |
| EINTR 추가 시도 | 0 |
| 전체 C 기록 수, 모든 파일 | 51,178개 |
| snapshot 임시 파일 C 호출 최대 | 81.934ms, 스레드 CPU 1.344ms |

C 기록의 시계 조회는 모두 가능했고 모든 관찰 호출이 성공했다. 건수는 복구·준비·측정·조회·drain을 포함한 서버 생애 분모이며 본 측정 요청 수와 같지 않다. 이전의 505ms WAL 호출은 이번 실행에서 재현되지 않았다.

가장 긴 WAL 호출은 경과 39.639ms 중 호출 스레드 CPU가 0.036375ms여서, 대부분을 해당 스레드의 CPU 실행 시간으로 설명할 수 없다. 이 호출을 감싼 OS 누계 구간은 201.959ms였으며 read 368,640 bytes·write 7,974,912 bytes가 증가했다. 이는 **같은 서버 프로세스의 구간 누계**다. concurrent snapshot 작업도 포함될 수 있고 WAL 한 호출의 물리 I/O 양이나 완료 시간으로 나눌 수 없다. 장치 flush·다른 커널 작업·스케줄링의 기여는 미확정이다.

서버의 유효 OS 표본은 3,022개, 간격 중앙값 204.658ms·최대 224.376ms였다. C 기록과 sampler는 같은 CLOCK_MONOTONIC으로 구간을 대조했다. read/write 누계는 감소하지 않았고 마지막 available=false/errno=3은 소유 서버 종료 뒤의 관찰이었다. 누계가 0인 구간도 I/O 대기나 전역 flush가 없었다는 증거로 쓰지 않는다.

## 실제 거래·복구 결과

본 측정은 예정 60,000건 중 59,998건을 실행했고 GENERATOR_LATE 2건으로 measurementValid=false를 보존했다. 요청 p99 20.366ms, 최대 150.071ms, 요청 또는 예정 기준 p99가 20ms를 넘는 5초 구간은 31/120개였다. 전송된 본 측정 요청은 모두 BUSINESS 응답이었다. 타이머 분리 실험과 실제 거래의 미전송은 별개 결과다.

최초 결과 51,384개·현재 주문 45,280개·체결 12,117개, 네 계정 잔고·공급량·호가·전체 최초 결과를 재시작 전후 비교했다. 조회 검증 후 기존 명령 12개를 재전송하고 전체 상태와 WAL 불변도 확인했다. 모두 일치했고 재시작→ready+health는 3.104초였다. 정상 재시작 검증이며 실제 전원 장애는 계속 보류다.

## 검증과 보관

타이머 도구의 경계/worker 합류·취소 테스트 2개를 race로 검사했다. overlay의 wal/bench/loadgen/exchange 네 패키지 기존 race·실제 HTTP/APFS 테스트를 통과했다. durable 패키지는 별도 테스트 파일이 없어 통과 건수로 세지 않는다. 6초 저장 예비 실행에서 133개 C 기록과 실제 프로세스 I/O 누계를 확인했고, 2초 타이머 예비 실행도 수행했다.

본 실행에서 타이머 120,000개 표본의 순번·예정 간격·경계 순서, trace 표시와 원본 offset을 대조했다. Go trace parser·본 거래의 Go 원본 재집계/진단 의미 검증, 전체 복구와 archive 모든 member 해시를 확인했다. 원본·binary·overlay·script·권한 probe·검증과 hash manifest는 고유 parent에 남긴다.

실행은 해당 parent의 `full_suite.py`, 분석은 `analyze.py`, 선택 실패 구간 추출은 `extract_timer_failure.py`다. 기존 출력 재사용은 거절하며 분석 출력이 있으면 bytes 일치를 확인한다. 파일·원본 재검증 뒤 보고서·집계·계획만 Git에 기록한다. 2026-11-03 실험 완료 당시에는 커밋·머지·push와 새 GitHub CI 실행을 하지 않았다.

## 남은 확인

HTTP·거래 엔진·WAL/snapshot 작업 없이도 50ms 검사 초과가 발생할 수 있는 경로를 확보했다. 다음은 같은 분리 도구에서 trace를 끈 대조 실행으로 계측 영향을 확인하는 것이다. 저장은 현재 환경에서 가능한 OS 누계 관찰을 마쳤으며 **커널 내부 I/O 추적은 관리자 권한을 확보한 별도 실행이 필요하다.** 이번 관찰을 근거로 타이머·매칭 엔진·저장 계약을 교체하거나 성능 개선을 주장하지 않는다.
