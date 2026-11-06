# 실행 trace·호스트 CPU/I/O 지연 진단

현재 저장소에서 다시 실행·검증·분석하는 명령은 [실험 도구 안내](../diagnostic-tools.md)를 따른다. 아래 경로·명령은 당시 측정 근거로 보존한다.

2026-11-03, [반복 측정](diagnostic-soak-repeats.md)에 이어 스냅샷 끔/켬을 각각 600초 실행했다. **50ms 초과 생성기 미전송은 재현되지 않았다. 10~40ms 전달 지연에서는 timer/select 대기와 runnable 실행 대기가 서로 다른 사례로 나타났고, 서버에서는 최대 505.110ms WAL sync를 관찰했다. 두 실행의 전체 복구는 일치했다.** 특정 엔진 최적화나 호스트 프로그램을 원인으로 확정하지 않는다.

[기계 판독 근거](20261103-trace-host-report.json)와 Git 제외 `var/trace-diagnosis-20261103.xu6tpijk`의 trace·호스트 원본·overlay·검증 결과를 함께 보존한다.

## 실험 구성

기반 revision은 `74a1206af94ada42eabbc5440e651c779bf181e5`다. production source를 수정하지 않고 Go build의 task-local overlay로 다음 관찰만 넣었다. 실제 overlay와 바이너리 hash는 protocol.json에 기록했다. 수정 없는 clean binary와 동일하다고 주장하지 않는다.

- 서버·생성기 main에서 execution trace 시작/종료, 시작·끝 UTC anchor. 초기화와 복구 검증용 재시작에서는 trace를 켜지 않는다.
- 생성기 예정 시각 대비 10ms 초과 시 scheduler/worker 관찰 시각·index를 trace.Log로 표시한다. 기존 시간 한도·전송 판단은 유지한다.
- WAL fileSink.Sync의 실제 호출을 `wal.fileSync` region으로 감싼다.
- trace는 파일당 512MiB로 제한하고 쓰기·sync·close 오류를 실패로 남긴다.

기존 config-v1/soak-10m-v1의 bytes, 100 requests/s·seed 17·warmup 5초·측정 600초·동시 한도 2,048·deadline 5초·발사 지연 한도 50ms를 유지했다. 서버·생성기 계측은 500ms, snapshot은 5초 주기다. 순서는 끔→켬 각 1회이며 전체 조회·복구·trace profile 변환·압축 후 30초 유휴했다. macOS 15.8 arm64/APFS, M3 Pro 11 CPU/18GiB, Go 1.27.3의 같은 개발 컴퓨터다. 기존 실험 자료와 미커밋 문서는 보존했다.

호스트 수집은 `iostat -d -C -w 1`의 전체 CPU와 disk0 전송량이다. 첫 부팅 이후 평균 행은 제외했다. 각 출력 행의 UTC·monotonic 수신 시각을 저장했다. 실제 간격 중앙값은 끔 1.005초·켬 1.005초, 최대는 1.023초·1.106초다. CPU 평균은 전체 코어를 합친 값이라 특정 코어 포화를 배제할 수 없고, 디스크 전송량은 개별 프로세스나 F_FULLFSYNC 완료 시간의 측정값이 아니다. 다른 프로세스의 이름·활동은 수집하지 않았다.

## 실행 결과

| 지표 | 끔 | 켬 |
| --- | ---: | ---: |
| 본 측정 예정 / 실행 | 60,000 / 60,000 | 60,000 / 60,000 |
| 생성기 미전송 | 0 | 0 |
| 요청 p99 | 7.368ms | 17.134ms |
| 요청 최대 | 54.784ms | 511.089ms |
| 20ms 초과 5초 구간 / 120 | 3 | 21 |
| 10ms 초과 scheduler/worker 표시 | 6 / 6 | 16 / 19 |
| 호스트 측정 표본 수 | 597 | 597 |
| 호스트 CPU busy 중앙값 / 최대 | 21% / 63% | 21% / 62% |
| 호스트 디스크 MB/s 중앙값 / 최대 | 9.64 / 199.68 | 10.93 / 169.32 |
| 전체 trace 중 WAL sync 20ms 초과 | 6 | 13 |
| 가장 긴 WAL sync region | 27.234ms | 505.110ms |

두 실행의 measurementValid는 true이고 전송된 요청은 모두 BUSINESS 응답이다. warmup 미전송·UNKNOWN은 없었다. 5초 초과는 요청 p99 또는 예정 기준 p99가 20ms를 넘는 구간이다. WAL region 건수는 준비부터 drain까지 전체 trace의 분모이며 600초의 요청 통계와 분모가 다르다. 표시 건수도 같은 요청의 scheduler·worker 표시를 각각 세므로 고유 요청 수가 아니다.

trace는 준비·warmup·부하·drain 및 조회 검증 시간까지 포함하고 실행 중 버퍼 기록·디스크 쓰기 비용이 있다. 이 결과는 진단 실행이며 무진단 baseline이나 처리량 개선으로 채택하지 않는다. 이전 6회보다 미전송이 줄었다는 사실도 최적화 효과로 해석하지 않는다.

## 지연 경계에서 확인한 사실

켬의 index 36016은 scheduler 관찰이 예정 시각보다 39.678ms 늦었다. 그 직전 goroutine은 select에서 약 43.646ms 기다렸고, Runnable→Running은 0.001920ms였다. 같은 요청의 worker는 예정 시각보다 40.460ms 늦었으며 마지막 runnable 대기는 0.765ms였다. 이 사례는 runnable 상태에서 수십 ms 기다렸다는 설명과 맞지 않으며, timer/select에서 runnable로 바뀌기 전 구간을 다음 진단 대상으로 좁힌다. OS 타이머·Go runtime·호스트 스케줄링 중 어느 원인인지는 이 trace만으로 구별하지 못했다.

반면 켬 index 35144의 worker는 예정 대비 18.678ms 늦었고 Runnable→Running 대기가 12.185ms였다. 실제 실행 대기 지연도 별도로 존재한다. 전체 생성기 scheduler profile의 누적 지연은 끔 약 3.837초·켬 약 3.562초이나, 이는 모든 goroutine과 전체 trace 구간의 합이며 개별 요청 p99나 CPU 사용 시간이 아니다.

약 40ms 사례 주변 호스트 표본의 CPU busy는 29~31%, disk0 전송은 11.44~39.96MB/s였다. 시스템 전체 CPU 포화가 보였다고 주장할 근거는 없지만, 한 코어·한 스레드의 일시 정지나 샘플 사이 경쟁은 배제할 수 없다.

서버에서 켬의 가장 긴 `wal.fileSync` region은 505.110ms였다. 주변 호스트 표본에는 CPU busy 45~62%, disk0 56.81~158.65MB/s가 기록됐다. 이 중 약 505.046ms는 Go의 Syscall 상태였고 반환 후 Runnable→Running은 0.054ms였다. UTC 대조에 ±30ms 여유를 둔 주변 요청의 최대 지연은 511.089ms였다. 서버 저장 호출 경계를 좁힐 근거이며, 특정 요청과 syscall의 일대일 인과 추적으로 해석하지 않는다. strict sync 호출의 벽시계에는 syscall/cgo 대기와 OS 스케줄링이 포함되며, aggregate I/O와 시간상 겹쳤다는 사실로 SSD 장애나 다른 프로세스의 원인을 확정하지 않는다.

## 복구·검증·시간 정렬

두 실행 모두 최초 결과 51,385개·현재 주문 45,280개·체결 12,117개, 네 계정 잔고·공급량·호가를 재시작 전후 전체 비교했다. 조회 검증을 마친 뒤 기존 명령 12개를 재전송하고 전체 상태와 WAL 불변도 확인했다. 모두 일치했다. 재시작→ready+health는 끔 1.125초·켬 2.710초였다. 실제 전원 장애 검증은 계속 보류다.

기존 bench/wal/loadgen/exchange 네 패키지의 overlay race·HTTP/APFS 테스트를 통과했다. 6초 예비 실행으로 trace 수집·호스트 수집·전체 복구를 먼저 확인했고, 두 본 실행의 Go 원본 재집계·dispatch/process 의미 검증을 수행했다. 네 trace는 `go tool trace -pprof=sched`와 전체 parsed event reader를 끝까지 통과했다. 원본 index·관찰 offset을 trace 표시와 대조하고 긴 sync의 상태 전이를 별도 추출했다.

UTC는 trace 시작 Log anchor에서 elapsed를 더해 대조한다. 시작/끝 anchor 사이의 UTC−trace 경과 차이는 끔 생성기/서버 약 +0.114/+0.120ms, 켬 -8.216/-8.466ms였다. 이 끝점 차이는 실행 중 전체 오차 상한이 아니다. 호스트 표본은 약 1초이고 수신 시각이므로 짧은 사건의 인과관계를 증명하지 않는다. trace의 Waiting→Waiting은 상태 재확인 이벤트일 수 있어 실제 새 대기 시작으로 세지 않았다.

## 보관과 재현

고유 parent에 protocol·overlay·binary·입력·trace와 metadata·scheduler profile·iostat 행·loadgen 원본·전체 복구 결과·snapshot/조회 archive를 보존한다. trace 파일은 약 39~51MiB로 각 512MiB 한도 안이다. 기존 원본은 수정하지 않았다. 실행은 해당 parent의 `suite.py --smoke`, 이후 `suite.py`, 분석은 `analyze_trace.py`와 `extract_sync.py` 순서다. 원본과 script hash를 함께 기록하며 재실험은 새 parent를 사용한다.

이번 작업은 진단·문서화이며 production source·dependency·저장 계약 변경은 없다. 2026-11-03 실험 완료 당시에는 커밋·머지·push와 새 GitHub CI 실행을 하지 않았다.

다음으로는 **같은 100/s 스케줄의 타이머 대기만 분리한 실험과, 긴 strict sync 구간의 OS I/O 관찰**을 별도 수행하는 것이 적절하다. 이번에 원래의 50ms 미전송을 재현하지 못했으므로, 재현을 확보하기 전에 타이머 구현이나 엔진을 교체하지 않는다.
