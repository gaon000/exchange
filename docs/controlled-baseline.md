# 현재 기준 반복 측정

기존 P6의 [고정 기준](benchmarks/p6-baseline.md#고정-비교-조건-v1)을 유지하며 현재 clean 생산 바이너리의 정상/snapshot 반복을 확인하는 절차다. 호스트 관찰과 시작 전 유휴 gate를 추가하며 전용 장비 수준의 완전한 격리나 성능 개선을 주장하지 않는다.

## 명령

먼저 source·문서 변경을 커밋해 작업 트리가 clean인지 확인한다. Go 1.27.3·Python 3.9 이상과 macOS arm64/APFS·cgo·AC 전원·OS 관찰 권한이 필요하다.

```sh
python3 tools/diagnostics/experiment.py baseline-build --output var/baseline-build
python3 tools/diagnostics/experiment.py baseline --build-dir var/baseline-build --output var/baseline-smoke --preset smoke
python3 tools/diagnostics/experiment.py baseline --build-dir var/baseline-build --output var/baseline-reference --preset reference
python3 tools/diagnostics/experiment.py verify --bundle var/baseline-reference --output var/baseline-reference-integrity.json
```

새 출력 이름을 사용한다. `baseline-build`는 현재 clean source에서 exchange/loadgen/experiment/benchreport/storageprobe를 빌드해 revision·실제 명령·Go build metadata·바이너리 SHA-256을 `build.json`에 고정한다. 이후 실행은 각 hash와 `vcs.modified=false`를 확인한다. 보관 실험의 바이너리를 자동 선택하지 않는다. 실험 실행기는 이 단계에서 직접 빌드한 생산 바이너리만 명시적으로 받는다.

`smoke`는 정상·snapshot 각 20 requests/s·warmup 없이 6초다. 경로·환경·원본 생성 점검이며 기준 측정으로 채택하지 않는다. `reference`는 기존 seed/초기 잔고/혼합/실행 한도를 유지하고 각 실행을 fresh run으로 준비한다.

| 조건 | 값 |
| --- | --- |
| 정상 요청률 | 29,000 requests/s |
| snapshot 요청률 | 3,000 requests/s |
| 기간 | 매회 warmup 5초 + 본 측정 20초 |
| 순서 | 정상1→snapshot1→snapshot2→정상2→정상3→snapshot3 |
| 반복·cooldown | 각 3회·실행 후 30초 (smoke는 0초) |
| 계측 | 서버 500ms, snapshot 5초 주기, 생성기 진단 0·trace/profile 없음 |
| Go 실행 환경 | GOMAXPROCS=논리 CPU 수, GOGC=100, GOMEMLIMIT=off, GODEBUG 제거 |
| 판정 | 기존 criteria-v1·benchreport. 실패 반복도 모두 포함 |

snapshot/정상의 요청률과 누적 상태 크기가 다르므로 두 조건의 비용 차이를 같은 상태에서의 snapshot 오버헤드로 해석하지 않는다. 이 실행 환경은 과거의 암묵적 Go 기본값과 별도 기록한 조건이며 이전 series와 자동 혼합하지 않는다.

## 요청률 옵션

reference의 기본 요청률은 정상 29,000/s·snapshot 3,000/s다. 다음처럼 두 요청률을 독립적으로 지정할 수 있다.

```sh
python3 tools/diagnostics/experiment.py baseline --build-dir var/baseline-build --output var/baseline-lower --preset reference --normal-rate 10000 --snapshot-rate 1000
```

reference 요청률은 1,000~80,000/s의 1,000/s 단위다. warmup+측정 25초에서 예정 시도 최대 200만을 유지한다. 높은 요청률도 자원 예산과 기존 판정을 통과해야 한다. smoke에는 요청률 override를 허용하지 않는다. 잘못된 값은 출력 생성·build 검증 전에 거절한다. protocol의 order, 개별 workload와 원본 manifest에 실제 요청률을 남기며 rate가 다른 실행은 별도 그룹이다. 실패한 rate를 삭제하거나 다른 rate의 통과로 교체하지 않는다.

## 호스트와 예산

각 실행 직전 AC 전원과 `pmset -g therm`을 기록한다. CPU 제한 지표가 있으면 100%를 요구한다. `iostat`의 since-boot 행을 제외하고 reference는 최소 10개 1초 관측에서 busy 중앙값 ≤15%, 최대 ≤30%여야 한다. smoke는 최소 2개를 관찰하며 실행 경로 검사이므로 quietGate.enforced=false로 기록한다. accepted=false인 관찰을 true로 바꾸지 않는다. reference의 gate 미달은 최대 세 번 보존하고 본 실행을 중단한다. OS/runtime의 순간 지연을 보장하는 조건은 아니다.

측정 중 별도 host sampler와 소유 프로세스 트리의 RSS·디스크를 관찰한다. 타 앱·시스템 프로세스를 종료하거나 OS 설정을 바꾸지 않는다. 호스트 관찰 자체의 비용도 0으로 가정하지 않는다. 다른 로컬 빌드·테스트·성능 실행을 겹치지 않고, user 작업·열 상태·캐시·OS 서비스를 완전히 격리하지 못한 한계를 기록한다.

여유 공간 8 GiB 이상, 새 묶음 4 GiB 이하, 단일 소유 프로세스 RSS 2 GiB 이하, 소유 프로세스 RSS 합계 3 GiB 이하를 주기적으로 검사한다. 주기 사이의 순간 증가까지 보장하는 제한은 아니다. 완료 코드 1인 무효 실험도 summary가 있으면 반복 목록에 보존한다. 예산·전원·quiet gate·기동/출력 실패는 중단하며 새 실패 묶음과 이미 생성된 원본을 남긴다. SIGINT/SIGTERM에는 소유 experiment의 종료와 자식 정리를 기다린다.

## 자료와 판정

parent에 `protocol.json`, `producer-build.json`, 호스트/quiet/power/guard 관측, 각 실행 argv·stdout/stderr와 workload를 기록한다. 개별 case는 기존 cmd/experiment의 환경·바이너리·입력·WAL/snapshot·표본·완료/실패 파일 형식이다.

`qualification.json`은 고정한 기존 benchreport가 각 case를 재검증한 결과다. `suite.status=COLLECTION_COMPLETED`와 parent 완료 hash는 자료 수집/기록 완료다. 성능 채택은 `cases.pass`와 조건별 `groups.qualified`를 확인한다. 수집 완료를 성능 통과로 해석하지 않는다. `verify --allow-failed`로 실패 묶음의 보존 상태만 확인할 수 있으며 completed=false로 남는다.

새 [비교 명령](experiment-comparison.md)을 쓸 때 P6 개별 case를 bundle로 선택하고 case 필드를 생략한다. 새 3회 반복을 과거 보관 자료와 비교하려면 그 자료도 원본 재검증·같은 조건·독립 반복 요구를 만족해야 한다. 기준 측정은 정상 재시작/실제 전원 장애의 추가 검증을 수행하지 않는다. 해당 검증은 별도 근거로 관리한다.

실제 실행의 결과와 한계는 [현재 기준 재측정](benchmarks/controlled-baseline.md)을 읽는다.
