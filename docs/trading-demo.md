# 한 명령 거래·복구 데모

macOS arm64·로컬 APFS·cgo 환경에서 저장소 루트에서 실행한다.

```sh
sh scripts/demo.sh
```

스크립트는 고정 Go toolchain을 확인하고 서버·데모를 빌드한 뒤, 기본 `all` 시나리오에서 거래 규칙과 정상 WAL·스냅샷·프로세스 강제 종료 복구를 자동 검증한다. 새 패키지 설치는 필요 없다. 서버는 숫자 loopback의 빈 포트로 시작하는 데모 소유 자식이며 기존 실행/외부 서버에 연결하지 않는다. 실제 자산을 사용하지 않는다.

## 시나리오 선택

| 옵션 | 검증 범위 |
| --- | --- |
| 기본 또는 `--scenario all` | 확장 거래 8명령·ID 충돌·정상 WAL 복구·snapshot 복구·SIGKILL 후 snapshot+WAL 복구 |
| `--scenario normal` | 기존 3명령 주문/부분 체결/취소·정상 WAL 재시작·최초 응답 재시도 |
| `--scenario snapshot` | 확장 거래·snapshot6 복구·추가 WAL1을 정상 재시작으로 복구 |
| `--scenario crash` | 확장 거래·snapshot 없이 승인된 WAL7을 소유 서버 SIGKILL 후 복구 |

```sh
sh scripts/demo.sh --scenario snapshot
sh scripts/demo.sh --scenario crash
sh scripts/demo.sh --scenario normal
```

각 실행은 새 `var/demos/demo-<고유값>/`를 사용한다. `--output var/my-trading-demo`로 기존 parent 아래 새 경로를 지정할 수 있다. 기존 폴더는 비어 있어도 거절하고 덮어쓰지 않는다. 빌드된 CLI는 `bin/demo --exchange bin/demo-exchange --scenario all`로도 실행할 수 있다. `bin/demo -h`에서 옵션을 확인한다.

## 전체 모드의 거래 규칙

Alice는 100,000 USDT, Bob은 1 BTC로 시작한다. 정수 lots/atoms로 검증하고 엔진/재생기로 기대값을 계산하지 않는다. 단위는 [API 계약](design-docs/api-v1.md#1-정수-단위와-한도)을 따른다.

| 순번 | 공개 HTTP 거래 | 독립적으로 확인하는 결과 |
| --- | --- | --- |
| 1 | Alice 0.01 BTC 매수 @60,000 | OPEN, 600 USDT 예약 |
| 2 | Bob 0.004 BTC 매도 @59,900 | 대기 매수 가격 60,000에 체결, Alice 잔량 0.006 BTC·360 USDT 예약 |
| 3 | Alice 잔량 취소 | 360 USDT 반환, 예약 0 |
| 4 | Bob 0.001 BTC 매도 @50,000 | 0.001 BTC 예약·매도 호가 유지 |
| 5 | Bob 0.001 BTC 매수 @50,000 | 자기 거래 차단으로 신규 매수 취소, 50 USDT 예약 반환·기존 매도 유지·체결 없음 |
| 6 | Alice 0.001 BTC 매수 @60,000 | 대기 매도 가격 50,000에 체결, 60 USDT 예약 중 50 USDT 사용·10 USDT 가격 개선 차액 반환 |
| 7 | 복구 뒤 Alice 0.0005 BTC 매수 @40,000 | OPEN, 20 USDT 예약. snapshot 이후 WAL에만 존재 |
| 8 | 다음 복구 뒤 Alice의 순번7 잔량 취소 | 20 USDT 반환, 예약 0 |

순번6과 최종 순번8의 기대 잔고는 Alice **0.005 BTC / 99,710 USDT**, Bob **0.995 BTC / 290 USDT**이며 모든 예약이 0이다. 순번7의 Alice 가용 USDT는 99,690, 예약 USDT는 20이다. 이 예약 잔고와 활성 호가가 복구 중 유지되는지 검사한다.

순번6 및 최종 순번8에서 기존 주문 ID에 다른 수량, 기존 취소 ID에 다른 대상 주문을 보내 **HTTP 409 IDEMPOTENCY_CONFLICT / NOT_ADMITTED**를 확인한다. 이후 전체 상태·최초 결과를 다시 조회해 순번/잔고/체결이 변하지 않았음을 검증한다. 고유 명령 한도는 8이며 한도 도달 뒤에도 같은 내용의 재시도는 최초 응답 bytes를 반환하고 충돌은 처리 순번을 늘리지 않아야 한다.

## 복구 순서와 증거

기본 all의 서버 수명은 다음과 같다. 모든 재기동 직후 **POST보다 먼저** GET으로 잔고·모든 생성 주문·호가·체결과 기존 명령의 최초 응답 bytes를 검사한다. 누락된 명령을 재시도 POST로 재실행해 복구 성공으로 보이는 일을 막는다.

| 서버 | 복구 기대값 | 확인 후 동작 |
| --- | --- | --- |
| 1 | 새 Genesis, snapshot0·재생0 | 순번1~6 실행, 충돌 확인, SIGTERM 정상 종료 |
| 2 | snapshot0·WAL6 | 전체 조회 후 100ms 주기 snapshot의 seq6 COMPLETED 이벤트 대기, 정상 종료 |
| 3 | snapshot6·WAL0 | 전체 조회 후 순번7 승인, 데모 소유 서버에 SIGKILL 후 Wait 합류 |
| 4 | snapshot6·WAL1 | 전체 조회로 순번7의 20 USDT 예약과 최초 OPEN 응답 검증, 순번8 취소·8개 재시도/충돌·정상 종료 |

snapshot 모드는 서버1에서 seq6 게시를 기다리며, tail 복구도 정상 종료로 확인한다. crash 모드는 snapshot을 켜지 않고 WAL7 복구를 확인한다. normal 모드는 기존 3명령과 snapshot 없는 WAL3 정상 복구를 그대로 유지하며 최종 잔고는 Alice 0.004 BTC/99,760 USDT, Bob 0.996 BTC/240 USDT다.

snapshot 이벤트는 단순 파일 존재 여부가 아니라 같은 runId·snapshotSeq=6·COMPLETED를 기다린다. 재시작 ready의 실제 선택 snapshot 파일/순번과 재생 수를 대조한다. 강제 종료는 직접 만든 자식의 SIGKILL 종료 상태와 로그 파일 닫힘을 확인한다. 다른 프로세스나 OS/장비 전원을 종료하지 않는다. **프로세스 강제 종료는 실제 전원 장애 검증이 아니며 powerLossTested=false다.**

## 출력과 보존

각 단계는 `[확인]`을 출력하고 모든 소유 서버 종료·파일 sync/close·summary 기록에 성공해야 `PASS`를 출력한다. 실패/중단/출력 오류는 종료 코드 1이고 이미 생성한 원본을 남긴다.

| 파일 | 내용 |
| --- | --- |
| `summary.json` | scenario, PASSED/FAILED, runId, 서버 SHA-256, 확인 항목/마지막 단계/오류, processKillTested, powerLossTested=false |
| summary의 `recoveries` | 서버 번호·선택 snapshotSeq·replayedCommands·verifiedBeforeRetry=true (확장 모드) |
| `http.jsonl` | 계정/요청/실제 HTTP 상태/원본 응답/전송 오류. 기대한 409도 보존 |
| `config.json`, `exchange` | 초기 설정과 모든 재시작에 사용하는 서버 사본 |
| `init.json`, `init.stderr` | 초기화 결과·오류 |
| `server-N.json`, `server-N.stderr` | 각 기동의 실제 ready/복구 보고서·오류 |
| `snapshot-events.jsonl` | all/snapshot 모드의 주기별 게시 결과 |
| `run/` | Genesis·WAL·snapshot 등 실제 모의 거래 파일 |

전체 제한은 빌드 이후 기본 2분이며 `--timeout`으로 최대 10분까지 지정할 수 있다. HTTP 요청은 5초, ready/snapshot 대기는 각 10초다. SIGINT/SIGTERM 또는 출력 오류 시 새 단계를 진행하지 않고 소유 서버를 정상 종료해 합류한다. 종료 10초 초과 시 해당 자식만 강제 종료하고 실패로 기록한다. 종료/저장 I/O의 합류 시간은 전체 제한에 추가될 수 있다.

## 자동 검증

```sh
scripts/go test -race ./cmd/demo ./tools/civerify -timeout=120s
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

일반 테스트는 예상 409/잘못된 HTTP 상태, response bytes 불일치, snapshot의 부분 이벤트/잘못된 identity·순번·outcome/취소, 자식 Kill/Wait와 기존 전송 오류를 검사한다. native 테스트는 all/snapshot/crash 실제 파일·서버·복구 경계·재시도 전 조회와 중단/출력 오류·잠금/포트 해제·기존 결과 보존을 검증한다. 기존 normal 검증도 유지한다. CI 필수 실행 검사에는 확장 native 테스트를 추가해 누락된 검증을 성공으로 간주하지 않는다.

이 데모는 성능 측정이나 실제 전원 장애 검증의 증거로 사용하지 않는다. 전원 장애 검증은 계속 보류다.


### 2026-11-12 검증 상태

기능 구현, 실제 한 명령 all 실행과 대상/전체 Go race 검증을 통과했다. 최초 portable CI는 마지막 진단 smoke의 최소 8GiB 공간 조건에서 실패해 보류했다. 이후 사용자의 검토·커밋·병합/푸시와 CI 확인 요청에 따라 여유 약 10GiB를 확인하고 전체 CI를 재개했다. 로컬 portable 210개·native race 243개(명시된 child-helper skip 1개), 각 Python 32개와 마지막 진단 smoke까지 모두 통과했다. 이전 실패 기록은 보존하며 실제 전원 장애는 계속 보류다. 상세 증거는 [완료 계획](exec-plans/completed/20261112-expanded-trading-demo.md)에 기록했다. 원격 CI는 push 뒤 해당 커밋의 실제 실행 결과로 확인한다.
