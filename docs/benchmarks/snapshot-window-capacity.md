# 스냅샷 실패 구간과 현재 정상 처리량

2026-11-07, 최근 요청률/재개 기능·정수 JSON 최적화 6개 커밋을 master d2d70ce로 반영했다. 기존 snapshot 실패의 마지막 응답까지 포함해 분석한 결과 같은 서버 관측 구간에 긴 WAL sync가 있었다. 정상 처리량 탐색은 두 요청률을 관찰한 뒤 디스크 자원 기준 때문에 중단됐다. 현재 최대 처리량·유효한 하한은 미확정이다. 실제 전원 장애는 계속 보류한다.

## 스냅샷 실패 구간 분석

[기계 판독 근거](20261107-snapshot-window-alignment.json)는 기존 snapshot 1,000/s 3회 원본을 benchreport로 다시 검증한 뒤 요청 cohort·snapshot 사건·서버 단계 차분·GC·coarse 호스트 UTC를 대조한 결과다. 원본 판정은 여전히 2/3 통과·미채택이며 변경하지 않았다. [이전 결과](lower-rate-integer-comparison.md)를 따른다.

| 실행 | 마지막 5초 요청 p99 | 마지막 응답의 창 밖 tail | 해당 cohort를 감싼 관측의 WAL sync >20ms |
| --- | ---: | ---: | ---: |
| snapshot1 | 39.254708ms | 11.727916ms | 1건 |
| snapshot2 | 12.193500ms | 6.193458ms | 0건 |
| snapshot3 | 9.733125ms | 5.756208ms | 0건 |

snapshot1의 가장 긴 요청은 epoch+24.919197458초에 시작해 25.006313125초에 끝났으며 지연은 87.115667ms다. 요청 지연은 예정 시각이 속한 20~25초 cohort로 판정하므로 측정 종료 뒤 끝난 응답도 포함한다. 서버 시각을 단순히 창 끝 이전 관측까지만 자르면 약 453ms의 tail을 놓쳐 WAL의 긴 호출을 제외할 수 있다. 새 분석은 마지막 응답까지 감싸는 다음 관측으로 확장했다.

가장 긴 요청들을 포함하는 서버 UTC 관측 구간은 **12:37:41.173598~41.675129**다. 이 구간에서 다음을 관찰했다.

- WAL sync 91건 중 20ms 초과 1건. 누적 최대가 10.369917→80.855292ms로 증가했다. append의 새 최대는 80.882792ms, command queue wait의 새 최대는 80.895875ms다.
- snapshot capture 1건 28.615791ms, 내부 copy 11.239208ms·sort 17.373667ms가 완료됐다. 이 시간과 WAL 지연을 더해 요청 지연을 분해하지 않는다.
- snapshot 게시 호출은 12:37:41.173472~41.675081, 내부 encode/write 431.517333ms, file sync 8.937583ms였다. 완료 표본이 같은 구간에 있다는 관찰이며 WAL 호출과의 정확한 일대일 시각은 없다.
- 서버 GC 3회, 병렬 GC CPU 합계 79.951675ms였지만 pause의 가장 큰 버킷 상한은 **0.04096ms**다. GC CPU를 같은 길이의 stop-the-world 정지로 해석하지 않는다.

실패한 마지막 5초를 감싼 전체 서버 관측에는 이전 capture도 포함돼 count=2이며, 앞 문단의 좁은 관측과 분모가 다르다. snapshot1의 해당 호스트 1초 관찰은 CPU busy 23~28%, snapshot2는 13~18%, snapshot3은 12~22%였다. since-boot 평균은 제외했다. 호스트 행이 긴 호출 자체를 감싸지는 않으며 전체 호스트·표시된 디스크의 coarse 값이므로 개별 요청/장치 원인으로 연결하지 않는다.

**긴 저장 경로 경과와 큐 지연이 함께 관찰됐다는 근거는 확보했지만 snapshot 게시·OS 스케줄링·디스크 중 단일 원인은 확정하지 않았다.** 500ms 누적 계측은 호출 시작 시각·개별 요청 연계·커널 내부 사건을 제공하지 않는다. 서버 관측 확장 구간은 인접 cohort/다른 작업을 포함할 수 있고 서로 겹친다. 끝점 UTC−monotonic 차이는 세 실행 각각 -0.326/-1.204/-1.203ms이며 전체 시각 오차 상한이 아니다. 생성기 GC는 전 생애 관측만 있어 같은 구간의 생성기 pause를 배제하지 않는다. 이 수준의 분석에는 기존 자료가 충분해 추가 진단 부하는 실행하지 않았다.

재현 도구는 [snapshot_windows.py](../../tools/diagnostics/snapshot_windows.py)이며 [사용법](../diagnostic-tools.md#스냅샷-창과-응답-tail-대조)을 따른다. Go 원본 재계산과 Python의 창별 요청 p99/건수를 다시 대조했고, 응답 tail·바깥 관측 누락 거절·host since-boot 제외를 포함한 Python 일반/`-O` 각 30개가 통과했다. 분석 출력 inventory도 검증했다. 입력 표본·WAL은 변경하지 않았다.

## 정상 처리량 탐색 조건

[사전 계획](../exec-plans/active/20261107-snapshot-window-capacity.md)의 20,000/s 시작·두 배 상한 탐색·1,000/s 해상도 축소를 따른다. 요청률별 독립 3회 전부를 같은 clean 생산 빌드로 실행하고 실패도 포함한다. warmup 5초·본 측정 20초·각 5초 창 p99 20ms·누락/운영 오류 0·기존 criteria-v1·500ms 서버 계측·snapshot 끔·trace/생성기 진단 끔·30초 cooldown과 전원/유휴/메모리/디스크 기준을 유지한다.

최고 채택점과 바로 위 실패점을 확인하면 이 호스트·시드·20초 조건의 관찰 경계로 보고한다. 모든 rate의 단조 통과나 전역 최대·장시간 보장을 주장하지 않는다. 아래에 실제 진행과 중단을 기록한다.


## 정상 탐색의 실제 진행과 차단

[기계 판독 진행 기록](20261107-normal-capacity-progress.json)에 고정 생산 빌드 7b9e720, 각 실행의 hash/판정/5초 창/호스트·마지막 자원 관찰과 inventory 검증을 보존한다. 7b9e720의 cmd/internal 생산 코드는 master d2d70ce와 같다. 추가된 것은 분석 도구·테스트·문서이며 정상 부하에는 진단을 켜지 않았다. master push의 [portable/native CI](https://github.com/gaon000/exchange/actions/runs/42245700854)는 모두 통과했다.

| 요청률·실행 | 전체 요청 p99 | 최악 5초 창 p99 | 판정 |
| --- | ---: | ---: | --- |
| 20,000/s, 1 | 7.675125ms | 7.984667ms | 통과 |
| 20,000/s, 2 | 9.071833ms | 12.397750ms | 통과 |
| 20,000/s, 3 | 22.109750ms | 29.249958ms | GLOBAL_P99·WINDOW_1/2/3_P99 실패 |
| 10,000/s, 1 | 7.184500ms | 7.248750ms | 통과 |
| 10,000/s, 2 | 7.190667ms | 7.228917ms | 통과 |
| 10,000/s, 3 | 16.451084ms | 30.083375ms | WINDOW_2_P99·UNSEALED_EXPERIMENT |

20,000/s는 독립 3회 2/3 통과·미채택이다. 10,000/s도 같은 생산 빌드로 새 3회 실행을 시도했으나 세 번째 실행의 자원 감시에서 여유 공간이 **8,544,423,936bytes**로 내려가 최소 **8,589,934,592bytes(8GiB)** 기준에 걸렸다. 같은 시점 새 묶음은 954,348,293bytes, 소유 프로세스 RSS 합계는 약 870MB로 다른 자원 상한 아래였다. 종료 처리는 오류 없이 합류했다. 거래 표본은 저장돼 measurementValid=true로 재집계됐지만 실험은 context canceled로 끝나 완료 봉인이 없으며, 이미 창별 지연 실패도 있다. 이를 완료된 유효 반복이나 용량 상한으로 채택하지 않는다.

10,000/s의 세 번째 실행 직전 유휴 검사는 9회 연속 거절된 뒤 통과했다. CPU 순간 busy는 91%까지 관찰됐다. 실제 실행 동안 호스트 busy 중앙값은 20,000/s 세 실행에서 31/33/80%, 10,000/s에서 16/17/16%였다. 프로세스 전체 생애의 coarse 관찰이며 서버만의 CPU나 독립적인 외부 부하를 분리하지 못한다. 시작 gate 통과가 실행 중 호스트 안정성을 보장하지 않았음을 남긴다.

**이번 새 탐색에서는 아직 qualified 하한이나 최고 처리량을 확보하지 못했다.** 이전 빌드 65ca1ba의 정상 10,000/s 3/3 결과를 이번 series에 합치지 않는다. 중단된 10,000/s 세 번째 실행을 지우거나 재시도로 대체하지 않는다. 두 collection의 완료/실패 inventory는 모두 검증했고 원본을 삭제하지 않았다.

사용자에게 다른 CPU 작업을 줄일 수 있는지, 기존 원본을 보존하면서 약 10GiB를 추가로 확보할 수 있는지 질문했다. 현재 추가 부하는 중단했다. 공간·호스트 조건을 확보하면 같은 빌드의 사전 계획상 다음 하한 5,000/s를 새 3회로 확인할 수 있다. 자원 중단 지점 10,000/s는 용량 상한으로 쓰지 않고, 유효한 실패 반복이 있는 20,000/s를 기존 상한으로 유지해 후속 탐색 범위를 정한다. 디스크 기준 완화·시스템 프로세스 중단·기존 자료 자동 삭제는 하지 않았다.

원본 재현 명령은 다음과 같다. 기존 경로를 덮어쓰지 말고 새 이름을 사용한다.

```sh
python3 tools/diagnostics/experiment.py baseline-build --output var/normal-capacity-build-20261107
python3 tools/diagnostics/experiment.py baseline --build-dir var/normal-capacity-build-20261107 --output var/normal-capacity-20261107-20000 --preset reference --normal-rate 20000 --condition normal --quiet-attempts 15
python3 tools/diagnostics/experiment.py verify --bundle var/normal-capacity-20261107-20000 --output var/normal-capacity-20261107-20000-integrity.json
python3 tools/diagnostics/experiment.py verify --bundle var/normal-capacity-20261107-10000 --allow-failed --output var/normal-capacity-20261107-10000-integrity.json
```

새 빌드 명령은 당시 revision에서 실행한 기록이다. 현재 source를 새로 빌드하면 실제 revision/hash를 새로 기록한다. 최종 처리량 경계·추가 rate 탐색은 미완료이며, 분석 도구 source의 원격 CI와 완료 master CI도 별도로 관리한다.
