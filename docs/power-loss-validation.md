# 실제 OS·전원 장애 검증 절차

상태: 절차 준비. 별도 시험 장비가 없어 실제 OS·전원 장애 시험은 미실행.
작성일: 2026-10-29 (Asia/Seoul).

[구현 계획의 P5](exec-plans/active/20261029-1531-exchange-v1.md#p5-공개-api와-장애경합-검증)는 성공 응답한 변경이 실제 OS·전원 장애 뒤에도 복구되는지 확인해야 한다. 현재 APFS 호출·저장 오류·프로세스 kill 테스트는 이 증거를 대신하지 않는다. 이 문서는 장비 확보 후 시험에서 사용할 관찰 방법과 작은 거래 사례의 판정 기준을 정한다. 장비 전원을 끄거나 OS를 중단하는 명령은 제공하거나 실행하지 않는다.

## 시험 장비와 관찰자 분리

- 시험 서버: 데이터 손실을 허용한 별도 macOS/APFS 장비. 서버 바이너리 revision/SHA-256, Go·OS·장치·파일 시스템·설정·runId를 기록한다. 자동 기동은 꺼 두어 장애 뒤 첫 복구 전에 원본을 보존할 수 있어야 한다.
- 관찰자: 시험 서버와 함께 전원이 끊기지 않는 다른 컴퓨터. 보낼 요청과 수신한 완전한 최초 응답, HTTP 상태, 실행 순서·시각을 이쪽 디스크에 남긴다. 서버에만 남은 로그를 성공 응답의 독립적인 증거로 쓰지 않는다.
- v1 서버의 loopback 전용 계약을 유지한다. 관찰자는 준비된 SSH 로컬 포워딩 등으로 시험 서버의 loopback에 접근할 수 있다. 서버를 외부 주소에 바인딩하거나 인증 없는 API를 공개하지 않는다. 장비·연결 정보와 접근 권한은 장비 확보 시 확정하며 현재 연결을 구성한 것은 아니다.
- 실제 중단 대상과 방식은 실행 전에 확정한다. OS crash, 실제 저장장치 전원 상실, 프로세스 kill, 정상 종료를 서로 다른 사건으로 기록한다. 배터리 장비의 충전기 제거만으로 실제 전원 상실을 주장하지 않는다.

사용자는 별도 장비가 없다고 확인했다. 현재 개발 컴퓨터는 관찰·문서 검증용이며 중단 대상이 아니다.

## 새 실험 준비

시험 서버에서 [로컬 설정](../config/examples/local.json)과 검증할 clean 바이너리를 사용한다. 다음은 새 데이터 디렉터리를 만드는 예시다. 실제 장비 시험을 재개할 때는 명령 실행 전에 대상 장비와 경로를 확인한다.

```sh
mkdir -p var
TEST_ROOT=$(mktemp -d var/power-check-XXXXXX)
bin/exchange init --config config/examples/local.json --data-dir "$TEST_ROOT/run" > "$TEST_ROOT/initialized.json"
bin/exchange serve --data-dir "$TEST_ROOT/run" --listen 127.0.0.1:18080 > "$TEST_ROOT/ready-before.jsonl" 2> "$TEST_ROOT/server-before.stderr"
```

ready 보고서의 runId·configHash·build·dataDir를 관찰자에 복사한다. 서버 시작 전 fresh run의 잔고는 alice/bob/carol 각각 BTC availableLots=10,000, USDT availableAtoms=100,000,000,000이고 예약분은 0이다. 조회 asOfSeq=0을 확인하고 다른 쓰기 클라이언트가 없음을 확인한다.

스냅샷을 포함하는 별도 실험에는 serve에 `--snapshot-interval 1s --snapshot-log "$TEST_ROOT/snapshots-before.jsonl"`을 추가한다. 새 실험끼리는 새 run을 사용한다. 일반 재시작에서는 같은 run을 사용하며 init이나 새 잔고 지급을 하지 않는다.

관찰자의 요청 기록은 다음 순서를 지킨다.

1. runId·accountId·메서드·경로·요청 ID와 정확한 본문을 전송 전에 새 파일에 기록한다. 기존 시도 파일을 덮어쓰지 않는다.
2. 요청을 보내고 수신 상태를 기록한다. HTTP 200, 완전한 JSON body, 해당 run/계정/kind/requestId와 commandSeq가 확인된 응답만 **관찰된 확정 결과**로 분류한다. 업무 거절도 보존해야 할 확정 결과다.
3. 응답 원문·상태·요청과의 연결을 관찰자 파일에 저장하고 flush·sync를 확인한다. 저장 실패면 그 시점의 시험을 유효한 증거로 보고하지 않는다. 다른 프로세스가 계속 보내는 요청을 숨기지 않는다.
4. timeout·본문 일부만 수신·연결 종료는 미확정으로 기록한다. 장애 시점 근처에 도착한 완전한 응답도 누락하지 않고 확정 집합에 포함한다.

기존 부하 생성기의 표본은 Run 종료 후 파일에 기록된다. 관찰자가 끝까지 살아 있고 표본을 완전히 저장했는지 별도로 확인해야 하며, 완료 표식 하나만으로 전원 장애 검증을 통과시키지 않는다. 아래 작은 사례는 순차 요청과 외부 응답 기록으로 판정을 단순하게 만든다.

## 고정 거래 사례

요청은 하나씩 보내고 1~4의 완전한 결과를 외부 관찰자가 보존한다. [API 계약](design-docs/api-v1.md)의 X-Run-ID·X-Account-ID와 Content-Type을 사용한다. 금액·수량은 정수 문자열이다.

| 순번 | 계정 | 메서드·경로 | 본문 |
| --- | --- | --- | --- |
| 1 | bob | POST `/v1/orders` | `{"clientOrderId":"power-maker","symbol":"BTC-USDT","side":"SELL","type":"LIMIT","timeInForce":"GTC","priceTicks":"100","quantityLots":"10"}` |
| 2 | alice | POST `/v1/orders` | `{"clientOrderId":"power-taker","symbol":"BTC-USDT","side":"BUY","type":"LIMIT","timeInForce":"GTC","priceTicks":"110","quantityLots":"4"}` |
| 3 | bob | POST `/v1/cancellations` | `{"cancelRequestId":"power-cancel","clientOrderId":"power-maker"}` |
| 4 | carol | POST `/v1/orders` | `{"clientOrderId":"power-reject","symbol":"BTC-USDT","side":"BUY","type":"LIMIT","timeInForce":"GTC","priceTicks":"100000000","quantityLots":"1000000"}` |
| 선택 5 | alice | POST `/v1/orders` | `{"clientOrderId":"power-pending","symbol":"BTC-USDT","side":"BUY","type":"LIMIT","timeInForce":"GTC","priceTicks":"90","quantityLots":"2"}` |

기대 최초 결과는 1의 ACCEPTED/OPEN, 2의 ACCEPTED/FILLED, 3의 CANCELLED, 4의 INSUFFICIENT_BALANCE다. commandSeq는 각각 1~4다. 2는 대기 주문 가격 100에 4 lots를 체결해 400 atoms를 이동하며, 3은 bob의 남은 6 lots 예약을 해제한다. 거절된 4도 재시작 후 같은 최초 결과를 가져야 한다.

실제 시험은 장비 확보 후 사전에 반복 수와 중단 조건을 정하고 다음을 구분해 수행한다.

- 확정 결과 보존: 1~4를 외부에 기록한 뒤 실제 장애를 발생시키고 복구한다.
- 미확정 명령: 1~4 뒤 선택 5를 전송하고 응답 대기 중 실제 장애를 발생시킨다. 5의 완전한 응답이 관찰됐다면 확정 명령으로 검사한다. 관찰되지 않았다면 복구에 포함될 수도, 없을 수도 있다.
- 스냅샷 조건: 별도의 새 run에서 주기 스냅샷을 켜고 수행한다. 생성/게시와 장애의 교집합을 입증하지 못했다면 단순히 옵션을 켰다는 이유로 ‘스냅샷 작성 중 장애’라고 기록하지 않는다. 소프트웨어 경계 주입 시험과 물리 시험의 관찰 범위를 분리한다.

명령 5가 미확정인 실험에서는 이후 쓰기 요청을 보내지 않는다. 장애 시도 때 서버가 정상 종료됐다면 실제 비정상 전원 상실 사례로 세지 않는다.

## 복구와 판정 순서

1. 서버가 아직 자동 복구하지 않은 상태에서 장애 후 run 디렉터리·WAL·스냅샷·임시 파일을 그대로 별도 보존하고 해시를 기록한다. 원본을 임의로 잘라내거나 초기화하지 않는다. 복사 실패나 파일 시스템 상태 때문에 자료를 확보하지 못하면 그대로 미확정/실패 사유로 남긴다.
2. 동일 바이너리와 같은 run 디렉터리로 serve만 실행한다. 새 경로의 ready/stderr 보고서를 남긴다. 복구 과정의 WAL tail 정리 전 사본을 남기는 이유는 복구가 원본을 변경할 수 있기 때문이다. 기동 실패도 결과이며 init으로 바꾸어 통과시키지 않는다.
3. ready의 runId·configHash·선택 snapshot·재생 수·건너뛴 후보를 확인한다. 이전 클라이언트/대기 요청을 종료하고 새 조회 연결만 사용해 결과를 관찰한다. **이 단계에서 주문이나 취소를 POST하지 않는다.**
4. 외부에 기록한 모든 확정 결과를 GET으로 조회한다. body의 최초 결과가 바뀌거나 사라지면 실패다. 결과를 먼저 POST로 재시도하면 유실된 명령을 새로 실행해 문제를 가릴 수 있다.
5. 잔고·현재 주문·호가·체결·asOfSeq를 확인한다. 확정 명령 외에 완전한 미응답 명령이 복구될 수 있으므로, 외부 확정 결과만 적용한 상태와 무조건 같아야 한다고 판단하지 않는다.
6. 위 읽기 전용 검사가 통과한 뒤에만 같은 키·내용으로 재시도한다. 최초 결과가 같고, 명령 순번·잔고·체결 수가 추가로 변하지 않아야 한다. 바뀐 내용을 같은 ID로 보낸 충돌 시험은 이 기록과 구분한다.

이 사례에서 첫 GET 목록은 다음과 같다. 각각 원래 계정 문맥으로 조회한다.

| 계정 | 경로 | 기대 |
| --- | --- | --- |
| bob | `/v1/results/place/power-maker` | 원래 seq=1 OPEN 응답. 현재 주문은 이후 체결/취소돼도 최초 응답은 같음 |
| alice | `/v1/results/place/power-taker` | 원래 seq=2 FILLED 응답 |
| bob | `/v1/results/cancel/power-cancel` | 원래 seq=3 CANCELLED 응답 |
| carol | `/v1/results/place/power-reject` | 원래 seq=4 INSUFFICIENT_BALANCE 응답 |
| alice | `/v1/results/place/power-pending` | 5가 확정이면 seq=5 필수. 미확정이면 아래 두 상태 중 하나 |

fresh recovery가 끝났고 다른 쓰기/이전 요청이 없는 이 고정 사례의 허용 상태는 두 개다. 일반 운영 중의 404 UNKNOWN을 미처리의 증명으로 일반화하지 않는다.

| 상태 | asOfSeq | alice BTC available/reserved | alice USDT available/reserved | 호가 |
| --- | --- | --- | --- | --- |
| 1~4만 복구 | 4 | 10,004 / 0 | 99,999,999,600 / 0 | bids/asks 모두 빈 배열 |
| 1~5 복구 | 5 | 10,004 / 0 | 99,999,999,420 / 180 | bids에 가격 90·수량 2 하나, asks 빈 배열 |

두 상태 모두 bob은 BTC 9,996/0, USDT 100,000,000,400/0이고 carol은 초기 잔고 그대로다. 자산별 합계는 초기 공급량과 같다. 체결은 `runId:2:1`, 가격 100, 수량 4, takerSide BUY 한 개다. 현재 power-maker는 executedQty=4, cancelledQty=6, status=CANCELLED이고 최초 seq=1 응답은 여전히 OPEN이다. 명령 5에 확정 응답이 있었는데 seq=4 상태로 복구되면 실패다. 알려지지 않은 추가 명령, 순번, 잔고 상태가 있으면 조건 위반 또는 복구 오류를 조사하며 통과로 처리하지 않는다.

## 보존할 결과와 증거 경계

각 실제 시험의 장비/OS/파일 시스템·소스/바이너리/config hash·runId, 외부 요청/응답 원본, 중단 방식·시각·관찰 근거, 복구 전 원본 사본과 해시, 첫 ready/복구 진단, **재시도 전** 조회 결과, 재시도 후 비교를 한 묶음으로 보존한다. 실패·미확정·실행하지 못한 시나리오도 제외하지 않는다. 수행한 장비·방식·반복 범위를 넘어 모든 저장장치나 전원 상실 시점의 보장으로 일반화하지 않는다.

2026-10-29에는 현재 개발 컴퓨터의 새 두 run으로 위 seq=4/5 사례를 실행하고 **자식 서버의 정상 종료·재시작**만 확인했다. 각각 10/11개의 읽기 전용 확인을 첫 재시도 전에 수행했고 최초 응답·기대 잔고·호가·체결·재시도 후 불변 상태가 일치했다. [예제 검증 자료](benchmarks/20261029-power-procedure-dry-run.json)에 실제 HTTP 요청/응답, runId, 바이너리 정보와 원본 경로를 기록했다. 이 자료의 actualPowerLoss와 separateObserverHost는 모두 false다. 실제 전원 장애, 미응답 요청의 물리 중단 경계, 다른 기계의 관찰자 연결은 검증하지 않았다.

장비 확보 전에는 실제 시험 결과를 기입하거나 P5/P7 전체 완료를 표시하지 않는다. 현재 준비한 절차와 소프트웨어 예제 검증은 하드웨어 시험을 재개하기 위한 준비 자료다.
