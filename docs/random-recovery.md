# 고정 seed 거래·복구 통합 검증

`tools/recoverycheck`는 짧은 거래 명령열을 실제 loopback HTTP와 APFS 저장소에서 실행한다. 주문·취소·동일 ID 재시도·충돌, snapshot과 정상 재시작을 고정 seed로 조합하고, 복구 후 현재 상태와 최초 응답을 비교한다. 새 성능 측정이 아니며 **실제 전원 장애 검증은 미완료·보류**다.

## 한 명령 실행·재현

저장소 루트, [지원 환경](development.md)의 Go·macOS arm64/APFS/cgo와 loopback 권한을 사용한다. 추가 의존성은 없다.

```sh
# 기본 seed 17, 총 64 actions. 자동으로 새 출력 폴더 생성
sh scripts/random-recovery.sh

# 명시적 seed와 새 출력
sh scripts/random-recovery.sh --seed 42 --actions 64 --output var/random-recovery-example42

# 실패 자료의 명령열을 그대로 새 폴더에서 실행
sh scripts/random-recovery.sh --replay var/random-recovery-example42/scenario.json
```

스크립트는 정확한 toolchain을 준비하고 현재 checkout에서 검사기를 빌드한다. 직접 사용하려면 `scripts/go build -mod=readonly -o bin/recoverycheck ./tools/recoverycheck` 후 `bin/recoverycheck --help`를 실행한다. 직접 바이너리를 사용할 때는 출력 parent를 먼저 준비한다. 스크립트는 기본 parent인 `var/scenarios`를 준비한다.

기본 출력은 `var/scenarios/<UTC 시각>/`이며 비어 있는 기존 폴더도 재사용하지 않는다. `--output`의 parent는 기존 실제 디렉터리여야 한다. 원본 run·봉인 묶음·기존 scenario 내부와 symlink parent에 출력하지 않는다. `/tmp` 같은 별칭은 `/private/tmp`로 풀어 지정한다. 입력 명세를 수정하거나 원래 run에 접속하지 않는다.

`--replay`는 seed 생성기를 다시 호출하지 않고 저장된 전체 action list를 읽는다. `--seed`/`--actions`와 함께 지정할 수 없다. 새 runId와 port를 발급하므로 실행 간 raw 응답 bytes가 동일하다는 의미는 아니다. **같은 실행 안에서** 최초 응답의 bytes가 재시도·재시작 후에도 일치해야 한다. seed·명령열은 결정적이지만 OS 시간·포트·실행 속도까지 고정하지 않는다. 버그 수정이나 환경 변화 뒤에는 같은 명세가 통과할 수 있다. 이전 코드의 실패를 조사할 때는 `build.json`의 revision/dirty 상태·Go 버전·바이너리 해시도 확인한다.

## 명령열과 비교 기준

생성 action은 32~256개이며 snapshot/restart control은 최대 32개다. 기본 실행은 64개다. `--replay`의 명시적 action list는 축소된 사례를 위해 1~256개도 허용한다. schema v1은 세 테스트 계정과 `config.json`에 기록한 고정 초기 잔고·정수 단위/한도를 사용한다. 시나리오를 바꿀 때 기존 실패 파일을 덮어쓰지 않고 별도 명세를 만든다.

필수 prefix는 여러 seed에서도 다음 경로를 반드시 포함한다.

- 매도 6 lot 중 2 lot 체결, 남은 4 lot 취소와 예약금 반환.
- 부분 체결 후에도 원래 OPEN 응답을 반환하는 동일 주문 ID 재시도.
- 주문 ID와 취소 ID 각각의 동일 내용 재시도·다른 내용 409 충돌.
- 자기 거래 차단과 잔고 부족 업무 거절.
- snapshot 순번 6 뒤 새 명령 한 건을 저장하고 재시작해 snapshot6+WAL1 복구.

그 뒤 seed가 정한 계정·가격·수량·취소 대상·과거 요청 재시도/변경·snapshot·재시작을 실행하고 마지막에는 반드시 재시작한다. 요청은 한 번에 하나씩 보내며 예상 밖 timeout/전송 실패는 전체 검사 실패로 보존한다. 이번 구현에는 동시 요청 스케줄링·의도적 응답 유실·SIGKILL 주입을 추가하지 않았다. 해당 기존 HTTP/프로세스 장애 검사는 계속 CI에 포함된다.

각 snapshot/restart에서 다음을 확인한다.

1. 실제 HTTP 결과가 순차 메모리 엔진의 결과·명령 순번과 일치한다. 중복/충돌은 새 WAL 순번을 쓰면 안 된다.
2. 모든 계정 잔고, 현재 주문, 공개 호가·체결과 저장된 최초 결과를 HTTP로 비교한다. 각 최초 결과는 GET과 원래 POST 재시도에서 처음 받은 bytes를 유지한다.
3. snapshot의 전체 checkpoint를 비교한다. 공개 호가 합계 외에도 FIFO 순서·모든 주문/체결·예약금/자산 공급량·최초 입력/결과 관계를 검사한다.
4. restart 전에 실제 WAL 레코드의 순서/입력을 기대 저장 명령과 대조하고 별도 순차 엔진에 재생한다. 기동 report의 선택 snapshot과 tail 재생 건수를 확인한다.
5. 별도 새 사본에 Genesis/WAL/빈 잠금 파일만 복사해 snapshot 없는 전체 복구를 실행하고 같은 HTTP/전체 checkpoint 검사를 반복한다. 원래 snapshot을 지우거나 원본 WAL에 재시도 기록을 추가하지 않는다.

restart의 선택 snapshot/tail을 확인한 **뒤**, 전체 상태를 읽기 위한 검증 snapshot을 명시적으로 게시한다. 이 snapshot은 다음 restart의 기준이 된다. 따라서 저장된 control action 외에도 검증 snapshot이 존재하며 결과 기록에 포함된다. WAL-only 사본도 전체 상태 검사 후 자체 snapshot을 생성한다.

순차 비교 엔진은 거래 서버와 같은 매칭 구현을 쓴다. 저장/전송/복구 경로 비교의 기준이며 독립 매칭 모델이라고 주장하지 않는다. 기존 `internal/engine/reference_test.go`의 독립 모델·fuzz 검사를 함께 유지한다. 처리량·지연 개선이나 전원 장애 내구성을 이 검사로 증명하지 않는다.

## 보존 자료와 실패

| 파일 | 내용 |
| --- | --- |
| `scenario.json`, `config.json`, `build.json` | 전체 action list, 고정 초기 설정, Go/플랫폼·VCS 상태·실행 바이너리 SHA-256 |
| `events.jsonl`, `http.jsonl` | 시작/성공 단계·복구 순번/상태 해시와 실제 HTTP 요청·상태·응답·오류 |
| `snapshot-*-{expected,actual}.json`, `restart-*-{expected,actual}.json`, `final-*.json` | 전체 checkpoint 기대값/관측값 |
| `restart-*.json` | 선택 snapshot·WAL tail 건수와 실제 runtime 복구 report |
| `run/`, `wal-only-*/` | 이번 실행에서만 생성한 실제 Genesis/WAL/snapshot과 별도 전체 재생 사본 |
| `summary.json`, `manifest.json`, `completed.json` | 단계 실행 요약, 파일별 크기/해시, 모든 종료·기록이 끝난 뒤 성공 봉인 |
| `failed.json` | 실패 step·오류·한 명령 재현 안내. 성공 완료 대신 생성하며 중간 자료 보존 |

기본 전체 context는 2분, HTTP 요청은 5초, 종료 대기는 5초다. SIGINT/SIGTERM은 취소와 정상 정리를 시도한다. 디스크/출력 오류도 성공으로 숨기지 않으며 기록 자체가 불가능한 경우 stderr와 기존 `scenario.json`/중간 자료를 사용한다. 실행 전 검증에서 거절된 명세/출력에는 새 폴더를 만들지 않는다. 실패 자료는 불완전할 수 있어 성공 봉인 여부와 구분한다. 기존 실패를 재현 성공으로 덮어쓰지 않는다.

CI native는 seed 17·42·701의 각 64 actions와 명세 replay·취소 실패 보존·순번 0 snapshot·의미 불일치 탐지를 검사한다. portable은 생성/입력/경로와 저장 미지원 시 무변경 실패를 검사한다. `scripts/ci.sh`는 테스트 전용 `EXCHANGE_SCENARIO_EVIDENCE_PARENT`를 지정해 소유 fixture를 `var/ci/<실행>/random-recovery-*/`에 보존한다. GitHub artifact에도 이 경로를 7일간 포함한다. 의도적 취소 검사의 `expected-cancelled-failure`는 검사 자체의 예상 실패 증거다. artifact를 내려받은 경우 그 안의 `scenario.json` 경로를 `--replay`에 지정한다.

구현·실제 실행 결과와 로컬 CI 근거는 [완료 계획](exec-plans/completed/20261112-random-recovery.md)에 기록했다.

CI artifact 수집은 선택한 테스트 자료 경로의 숨김 파일도 포함한다. 실제 run과 WAL-only 사본의 `.lock`이 봉인 목록에서 빠지지 않도록 보존하며, 저장소 전체나 별도 사용자 파일을 수집하는 설정은 아니다.
