# 최신 스냅샷 읽기 경로 프로파일링

최근 [이후 명령 복구 실험](snapshot-tail-recovery.md)의 같은 active/history 각 1만 명령 최종 snapshot/WAL을 사용한다. 최신 checkout의 공개 `runtime.Open`에서 CPU와 allocation을 별도 프로세스로 수집하고, pprof 호출 경로 focus와 기존 세부 stage로 읽기 비용을 나눈다. tail=0이며 생산 코드/기존 Go profile probe를 바꾸지 않는다. [실제 분석](benchmarks/snapshot-read-profiling.md)을 함께 읽는다.

## 명령

저장소 루트에서 macOS arm64/APFS/cgo·프로젝트 Go wrapper·Python 3 표준 라이브러리를 사용한다.

```sh
# 두 상태 각각 CPU 20회·allocation 5회, 총 50개 새 worker
python3 tools/diagnostics/snapshot_read_profile.py run --bundle var/snapshot-tail-full-20261113 --output var/snapshot-read-profile-new

# 작은 실제 기능 확인: 24명령 자료, 각 한 번·총 4개 worker
python3 tools/diagnostics/snapshot_read_profile.py run --smoke --bundle var/snapshot-tail-review-smoke-20261113 --output var/snapshot-read-profile-smoke-new

# 봉인 검증 뒤 기존 profile로 새 JSON/Markdown·함수표 생성
python3 tools/diagnostics/snapshot_read_profile.py analyze --bundle var/snapshot-read-profile-new --output var/snapshot-read-profile-analysis-new
```

새 clone에는 `var`가 없다. 먼저 `snapshot_tail.py run` 또는 `run --smoke`로 자료를 만들거나 보관한 해당 원본이 필요하다. run은 입력 전체 봉인·기존 30회(작은 자료 6회) 복구 검증을 다시 읽고, 현재 생산 source가 입력 측정과 같은지 확인한다. 추적 변경뿐 아니라 새 미추적/ignore 생산 파일도 거절한다. 도구 변경은 허용하고 source archive/바이너리 SHA·revision·dirty·환경을 따로 기록한다.

analyze는 입력 원본 경로에 접근하거나 보관된 probe 바이너리를 실행하지 않는다. 묶음 안에 보존한 두 fixture/개별 profile/바이너리와 현재 Go `tool pprof`를 사용한다. 같은 toolchain을 사용해야 하며 raw profile에서 CPU 10ms period도 확인한다.

## 수집과 검증

CPU는 기본 메모리 sampling 512KiB에서 20회씩, allocation은 `MemProfileRate=65536`에서 5회씩 수집한다. CPU와 allocation을 함께 켜지 않는다. 반복마다 상태 순서를 교대하고 각 worker는 새 사본/프로세스다. 선택 snapshot 순번=최종 명령 수, 재생 0·fallback 부재를 요구한다.

Open 이후 profile을 끝낸 뒤 공개 Snapshot으로 전체 최종 상태 bytes를 다시 확인한다. 최초 결과·주문·체결·잔고를 포함한 canonical 파일이 기준과 같아야 한다. Genesis/WAL/lock와 snapshot 원본/worker 사본도 불변이어야 한다. profile은 별도 파일로 보존하며 이전 결과나 실패를 덮어쓰지 않는다.

출력 parent는 기존 디렉터리여야 한다. 기존 출력/symlink/입력 내부 출력을 거절한다. 생성/빌드 전 128MiB 준비 공간, fixture/worker 복사량과 추가 profile 예산·4GiB 묶음 상한·별도 디스크 여유 8GiB를 검사한다. 원본/고정 빌드를 삭제하는 명령은 없다.

## 비용을 읽는 방법

| focus | 포함하는 호출 경로 |
| --- | --- |
| wholeOpen | 공개 Open 중 전체 CPU/할당 표본. GC·runtime/profile 관련 비용이 남을 수 있음 |
| snapshotRead | snapshot `readWithTimings`를 거친 envelope/checksum·decode 호출 |
| snapshotDecode | `decodePayloadWithTimings`를 거친 leaf/배열/정수·canonical 비교 |
| restore | 엔진 RestoreWithTimings의 상태/호가/결과 캐시 복원·검증 |
| confirm | 선택 snapshot의 canonical 재확인·기존 파일 sync |
| wal | WAL Open/프레임 검사·prefix 재검사·복구 sync 등 WAL 경로 |

focus는 같은 표본을 중복 포함할 수 있다. read 안에 decode가 있고 Open에 나머지가 들어가므로 합산하지 않는다. `topFlat`은 함수 자체, `topCumulative`는 하위 호출 포함 비용이다. 전체 함수표·alloc_objects·caller 관계·합친 protobuf는 analysis에 보존한다. JSON에는 scope별 합계/호출 수와 상위 함수, 정확한 API TotalAlloc 증가, 기존 세부 stage의 중앙값/최소/최대를 남긴다.

CPU ms/호출은 표본 합/호출 수이며 wall 중앙값이나 I/O 대기 시간이 아니다. allocation MiB/호출은 두 GC로 accounting을 반영한 전후 profile 차이의 64KiB 샘플링 추정치다. 프로파일 직렬화/GC/런타임 부수 할당은 전역 차이에 남을 수 있어 내부 focus와 정확한 API TotalAlloc counter를 구분한다. 유지 heap/RSS 또는 누수 측정으로 해석하지 않는다. [기존 profile 계약](snapshot-profiling.md), [Go 진단 지침](https://go.dev/doc/diagnostics), [pprof](https://pkg.go.dev/runtime/pprof)를 따른다.

`load.checksum`·`load.decode`·`decode.orders/book/trades/results`·Restore/replay/Confirm의 stage wall도 profiler별로 따로 집계한다. 이 값은 계측 중 관찰값이므로 기존 비계측 latency나 최적화 전후 성능 수치와 섞지 않는다. CPU의 함수별 비용과 checksum/decode 단계 wall은 다른 측정량이다.

짧은 smoke에서 CPU 표본 0은 기능 검증만 통과시키고 비용 추론을 하지 않는다. full 수집에서는 전체/읽기/디코딩에 유효 표본이 있어야 한다. 다른 focus가 0이면 그 경로의 비용이 없다고 결론 내리지 않는다.

사본/해시 뒤 warm-cache이며 CPU 유휴 gate·cache flush를 하지 않은 비격리 개발 호스트다. profile 밖에 준비/사본/검증용 publication/종료를 두며 공개 API 내부 자연 GC·I/O는 포함한다. HTTP p99·최대 처리량·cold-cache/디스크 지연의 인과 효과·실제 OS/전원 장애를 검증하는 실험이 아니다. 실제 전원 장애는 **미완료·보류**다.
