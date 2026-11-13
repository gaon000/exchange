# 상태 크기별 메모리·snapshot·복구 비용

`tools/statescale`와 `tools/diagnostics/state_scale.py`는 같은 명령 수의 다른 상태 구성을 만들고, 동일 fixture 사본의 새 프로세스에서 비용과 완전한 복구 일치를 확인한다. 거래 production 코드를 바꾸지 않는다. 1,000/5,000/10,000 명령 × 활성/이력형 × 5회 × 두 복구 경로로 총 60 worker를 실행한다.

## 실행과 재집계

[개발 환경](development.md)의 정확한 Go와 Python 3.9 이상 표준 라이브러리, macOS arm64/APFS/cgo를 사용한다. `sysctl`로 장비 정보를 읽는다. 외부 Python/Go dependency 설치는 없다.

```sh
# 12명령, 각 상태 1회/두 복구 경로의 기능 smoke
python3 tools/diagnostics/state_scale.py run --smoke --output var/state-scale-smoke-new

# 요청한 전체 6조건 × 5회, 총 60 worker
python3 tools/diagnostics/state_scale.py run --output var/state-scale-full-new

# 원본 전체 봉인을 확인하고 새 폴더에 JSON/Markdown 재생성
python3 tools/diagnostics/state_scale.py analyze --bundle var/state-scale-full-new --output var/state-scale-reanalysis-new
```

출력 parent는 미리 존재해야 한다. 빈 폴더도 재사용하지 않으며, 기존 run/봉인 자료 내부나 symlink parent에 쓰지 않는다. 출력 `statescale`은 이번 실행에서 현재 checkout으로 빌드한 동결 probe다. 부모 조정기는 이 SHA-256을 계속 대조하고 같은 바이너리만 실행한다. 과거 보관 생산 바이너리를 불러 실행하는 기능은 없다. 새 원본/실패를 덮어쓰거나 자동 삭제하지 않는다.

## 상태 구성

| 유형 | N개 명령 뒤 주문/활성/체결/최초 결과 | 구성 |
| --- | --- | --- |
| active | N / N / 0 / N | 두 계정의 가격 9,900 매수·10,100 매도, 각 1 lot. 서로 체결되지 않는 두 가격 단계에 주문과 FIFO 누적 |
| history | N/2 / 0 / N/4 / N | 매도 2 lot→매수 1 lot 부분 체결→매도 잔량 1 취소→체결된 매수 취소 거절의 4명령 사이클. 취소 성공·거절 이력도 저장 |

표의 price는 정수 priceTicks다. 초기 자금은 N에 맞춰 충분히 공급하고 maxCommands=N으로 제한한다. 모든 명령 ID는 고유하다. active는 실제 활성 주문 수를 크게 만들지만 가격 단계 수까지 늘리는 실험은 아니다. 같은 N이어도 주문/체결/결과 payload 구성은 다르므로 liveness 하나의 인과 효과로 해석하지 않는다.

fixture는 순차 엔진과 256명령 이하 WAL 배치를 사용해 준비하고 전체 불변식·구성 수를 확인한 뒤 기준 snapshot을 게시한다. 준비 속도는 거래 HTTP 처리량으로 보고하지 않는다. 각 fixture의 Genesis/WAL/lock/snapshot bytes와 runId는 모든 반복에서 같다.

## 측정 순서와 범위

반복별 조건 시작 위치를 회전하고 순서를 뒤집는다. 홀수 반복은 WAL→snapshot, 짝수는 snapshot→WAL 순서다. 매 worker는 새 프로세스와 새 run 사본을 사용한다. 복사·해시로 파일 cache가 준비되며 OS cache flush는 하지 않는 **warm-cache 측정**이다. 완전히 격리한 호스트라고 가정하지 않는다.

- WAL 경로: snapshot 없이 `runtime.Open` 전체 복구 → GC/메모리 관찰 → 새 `runtime.Snapshot` 생성.
- snapshot 경로: 최종 순번 N의 snapshot+전체 WAL에서 `runtime.Open` → GC/메모리 관찰 → 같은 snapshot bytes의 재확인 publication.
- snapshot 경로의 tail은 0이다. 전체 WAL prefix 검사·identity/의미 검증·strict sync를 생략하지 않는다. tail 길이에 따른 비용은 이번 범위 밖이다.
- `Open`과 `Snapshot` 공개 API의 monotonic wall 시간을 기록한다. 입력 준비·사본·해시·강제 GC·후속 상태 검증은 이 wall 시간 밖이다. timed call 내부의 자연 GC/스케줄링/파일 동기화 대기는 포함된다.
- 기존 stage 계측의 capture.copy/sort, encode/write/file sync/directory sync, snapshot load/rebuild/WAL 대조/확인 시간을 함께 남긴다. 중첩 stage와 각 행 중앙값을 합쳐 전체 시간으로 사용하지 않는다.
- 매 worker는 검증 단계에서 생성/재확인한 canonical snapshot 파일 SHA-256을 기준과 대조하고 WAL/Genesis/lock 불변도 확인한다. 전체 주문·계정·잔고·FIFO·체결·최초 명령/결과를 포함한 완전한 파일 bytes 일치다. 전체 HTTP 조회를 순회하는 추가 비용은 측정하지 않는다.

## 메모리 해석

`runtime.ReadMemStats`의 HeapAlloc/HeapInuse/HeapSys/TotalAlloc/Mallocs/NumGC 원본을 Open 전, 직후, 강제 GC 뒤와 Snapshot 전/직후/GC 뒤에 보존한다. runtime 객체는 GC 동안 살아 있다. Go heap 증가 값은 Open 뒤 GC 후 HeapAlloc에서 Open 전 GC 후 값을 뺀 값이다. 엔진만의 크기가 아니라 WAL/runtime/계측을 포함한 유지 메모리이며 작은 음수도 0으로 숨기지 않는다. TotalAlloc 차이는 해당 공개 API의 할당량으로, peak live heap과 다르다.

Darwin `getrusage(RUSAGE_SELF).ru_maxrss`는 [현재 Darwin 문서](https://github.com/apple/darwin-xnu/blob/main/bsd/man/man2/getrusage.2)의 bytes 단위를 사용한다. Open 뒤 RSS와 Snapshot 뒤 RSS 모두 **worker 시작 이후 누적 high-water**다. 후자는 후속 검증 전에 읽는다. 둘의 차이를 Snapshot만의 peak 메모리라고 해석하지 않는다. 프로세스 RSS는 Go heap과 allocator/stack/code 등의 차이가 있으므로 같은 수치라고 가정하지 않는다.

GOMAXPROCS=호스트 논리 CPU 수, GOGC=100, GOMEMLIMIT=2GiB를 고정하고 기록한다. GOMEMLIMIT는 Go의 soft limit이며, 측정 뒤 누적 RSS가 2GiB를 넘으면 해당 worker를 거절한다. 입력은 최대 10,000명령, 전체 자료는 4GiB, 디스크 여유는 예상 추가 복사/생성량과 별도로 8GiB를 보존한다. 준비 뒤 실제 fixture bytes로 전체 추가 쓰기량을 다시 검사한다. 부족하면 원본을 지우지 않고 실패를 남긴다.

## 자료와 검증

`protocol.json`에는 순서/반복/GC/cache 조건, `build.json`과 `source.tar.gz`에는 기준 revision·dirty 상태·Go/하드웨어·실제 source/binary 해시를 남긴다. source archive는 Go/Python/설정·실행 스크립트의 당시 bytes이며 docs 편집과 구분한다. fixture별 receipt는 원본 파일 SHA-256·정확한 구성 수를 포함한다. `runs/NNN`은 case·probe stdout/stderr·raw JSON과 해당 run 사본을 보존한다.

`report.json`은 두 복구 경로별 메모리/전체·세부 시간과 모든 조건의 count/median/min/max를 보존한다. `report.md`는 상태 구성/파일 크기·시간/heap·snapshot 단계/할당량/RSS 비교표다. 느린 표본을 제외하지 않는다. 정상 완료는 전체 파일 봉인을 게시하며 `analyze`는 봉인·동결 빌드·fixture/사본 해시, 순서/반복 수·recovery path·publication 종류·GC/메모리 값까지 재검증한다. 잘못된 입력/누락/실패를 0으로 채우지 않는다. 실패 출력은 `failed.json`과 중간 자료를 보존한다.

Go의 생성/정수 기대값·실제 native 복구/소유권/bytes 검증과 Python의 반복/누락/조건/원본 변조/보고서/예산 회귀를 검사한다. CI는 12명령/4worker smoke만 실행하며 큰 본 측정은 CI에 포함하지 않는다.

이 결과는 HTTP p99·최대 지속 처리량·운영 SLA·일반 장기 운전의 보장이 아니다. **실제 OS·전원 장애 검증은 미완료·보류**다.

2026-11-13의 6조건 5회 실제 결과와 한계는 [측정 보고서](benchmarks/state-size-costs.md), raw 집계는 [JSON](benchmarks/20261113-state-size-costs.json)을 따른다.

실행/검증 근거는 [완료 계획](exec-plans/completed/20261113-state-size-costs.md)에 기록했다.
