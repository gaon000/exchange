# v1 단위·입력·HTTP 계약

상태: Accepted (P0 구현 기준)
결정일: 2026-10-29 (Asia/Seoul)
구현 상태: P5의 [HTTP 계층](../../internal/api/server.go)을 runtime에 연결했다. 엄격한 입력·계정/run 문맥·오류 구분·최초 응답·조회·체결 페이지와 실제 loopback 거래/재시작을 검증했다. 초기화·서버 실행 CLI와 남은 장애 통합 검증은 진행 중이다.

[제품 명세](../product-specs/exchange.md)의 정책과 [처리 설계](exchange-v1.md)를 구체화한다. 여기서 정하는 숫자는 재현용 모의 거래 설정이며 실제 시장 규칙·시세가 아니다. 변경 시 Genesis의 설정 식별값과 규칙 버전을 함께 검토한다.

## 1. 정수 단위와 한도

v1 거래쌍은 `BTC-USDT`다. 내부 가격·수량·잔고·대금은 부호 있는 64비트 정수다. wire에서는 정밀도 손실을 피하기 위해 정수 **문자열**로 주고받는다.

| 값 | 정의·범위 |
| --- | --- |
| quantityLots | 1 lot = 0.0001 BTC. 주문은 1~1,000,000 lots (최대 100 BTC) |
| quoteAtoms | 1 atom = 0.000001 USDT |
| priceTicks | 1 lot의 가격을 quoteAtoms로 표시. 1~100,000,000 |
| 표시 가격 | priceTicks / 100 USDT/BTC. 따라서 가격 tick은 0.01 USDT/BTC |
| 주문 최대 대금 | priceTicks × quantityLots ≤ 100,000,000,000,000 atoms |
| 초기 base 전체 공급량 | 0~1,000,000,000,000 lots |
| 초기 quote 전체 공급량 | 0~1,000,000,000,000,000,000 atoms |
| commandSeq, createdSeq, updatedSeq | 1~2^63−1. 초기 상태의 asOfSeq/snapshotSeq는 0 |
| ID | 계정·clientOrderId·cancelRequestId는 ASCII `[A-Za-z0-9_-]{1,64}`, 대소문자 구분 |
| runId | 새 초기화 때 생성하는 소문자 UUID v4. 일반 재시작에서 유지 |
| 요청 본문 | UTF-8 JSON object, 최대 4,096 bytes. 압축 본문 미지원 |
| 계정 수 | 초기화 때 1~10,000개, ID 중복 금지. 실행 중 추가 없음 |
| 실험 크기 | 기본 최대 1,000,000개 고유 명령. Genesis 설정에 고정하며 중복 재시도는 제외 |

곱셈 전 `p <= MaxInt64/q`, 덧셈 전 `a <= MaxInt64-b`를 확인하고 위 업무 한도도 적용한다. 초기 자산별 합계 한도 때문에 전체 잔고가 한 계정으로 모여도 범위를 넘지 않는다. 자산별 예약·이동은 공급량을 늘리지 않는다. 전체 공급량 0은 거래 거절 테스트에 사용할 수 있다.

예: BTC당 60,000 USDT는 `priceTicks="6000000"`, 0.01 BTC는 `quantityLots="100"`다. 최대 예약 대금은 600,000,000 atoms = 600 USDT다. 표시값 환산을 엔진의 정수 계산에 역으로 끼워 넣거나 float로 반올림하지 않는다.

[로컬 초기 설정 예시](../../config/examples/local.json)는 alice/bob/carol 각 1 BTC와 100,000 USDT를 지급한다. 합계는 base 30,000 lots (=3 BTC), quote 300,000,000,000 atoms (=300,000 USDT)다. `reserved=0`에서 시작한다. P1 도메인 테스트에서 이 fixture의 값·한도를 확인한다. P2는 엄격한 Config decoder와 Genesis 초기화 API를 제공한다. 서버 설정 CLI loader는 P5에서 연결한다. 계정 수·잔고는 허용 한도 안에서 새 실험마다 바꿀 수 있고, 단위와 주문 한도는 v1 규칙이다.

## 2. 로컬 전송과 계정 문맥

- HTTP/1.1 + JSON, 기본 주소 `127.0.0.1:8080`. v1은 loopback 바인딩만 허용한다. 외부 접속·TLS·실서비스 인증은 범위 밖이다.
- `/v1/` 요청은 `X-Run-ID`와 `X-Account-ID`가 각각 하나씩 필요하다. 계정은 현재 Genesis의 등록 ID와 대조하고, 본문에 accountId를 받지 않는다.
- 헤더 기반 테스트 계정 선택은 같은 컴퓨터의 신뢰한 CLI/loadgen을 위한 것이다. 다른 로컬 프로그램의 사칭을 막는 인증으로 설명하지 않는다.
- Host는 숫자 IPv4/IPv6 loopback 주소만 허용한다. `127.0.0.1:8080`, `[::1]:8080`은 가능하며 DNS 이름·zone·wildcard는 받지 않는다. listen도 같은 규칙이며 hostname을 해석하지 않는다. 브라우저 Origin 헤더가 있는 요청은 빈 값이어도 거절한다. CORS를 열지 않는다.
- JSON 요청의 Content-Type은 `application/json` (선택적인 charset=utf-8 허용). Content-Encoding은 없거나 identity만 허용한다.
- 기본 연결 요청 제한: 요청 줄과 초기 헤더/종료 개행 합계 8 KiB, 디코딩한 본문 4 KiB, 헤더 읽기 2초, 본문 읽기 2초, 결과 대기 5초, 응답 쓰기 5초. 고정 Go 1.27.3의 net/http가 더하는 4 KiB 읽기 여유분을 보정하고 8,192/8,193-byte 실제 경계로 검사한다. 이는 서버 자원 한도이며 p99 성능 목표가 아니다.
- command queue 기본 8,192개, 동시에 대기/송신하는 HTTP 요청 최대 16,384개, 고유 pending 키당 대기자 최대 16개. query도 전역 요청 한도를 공유하며 업무 고루틴용 별도 query queue 기본 1,024개를 둔다. P3의 runtime.Options로 노출했으며 P6 측정에 기록한다.
- 포화 거절과 연결 timeout이 이미 접수한 명령의 처리 취소를 뜻하지 않는다. 준비 완료 전에는 변경 요청·업무 조회·완료 캐시 응답을 제공하지 않는다.

[api.New](../../internal/api/server.go)는 소켓을 열지 않고 handler와 전송 한도를 구성한다. `ListenLoopback`과 `Server.Serve`로 연결하고 `Server.Shutdown`으로 HTTP 접수를 닫는다. 프로세스를 조립하는 호출자는 별도로 runtime도 종료해야 한다. HTTP 요청 한도는 본문 검사·결과 대기·응답의 마지막 flush까지 유지하고 포화 시 즉시 429를 반환한다. body가 없는 조회의 연결 감지 읽기에 본문 deadline을 설정하여 결과 대기를 단축하지 않는다. 본문을 받는 메서드는 POST 두 개뿐이며 GET 본문은 거절한다.

## 3. 정규화와 중복 키

정수 문자열은 ASCII 숫자 1~19자리만 허용한다. 앞의 0은 제거하여 `"000100"`과 `"100"`을 같은 값으로 비교한다. 부호·소수점·지수·문자열 안 공백·JSON number·null은 거절한다. 0 허용 여부는 필드별 범위를 따른다. JSON 바깥 공백과 필드 순서, 동일 문자를 표현하는 JSON escape는 의미를 바꾸지 않는다.

모든 필드는 명시적으로 필요한 것만 허용한다. JSON 키는 대소문자를 구분하고, 중복 키·미지 필드·누락·후행 JSON 값·잘못된 UTF-8을 거절한다. Go `encoding/json`의 기본 대소문자 무시나 중복 키 덮어쓰기에 그대로 의존하지 않는다. 심볼·side·type·timeInForce는 아래 표의 대문자 literal만 허용한다.

- PlaceOrder 키: `(runId, accountId, PLACE_ORDER, clientOrderId)`.
- CancelOrder 키: `(runId, accountId, CANCEL_ORDER, cancelRequestId)`.
- 비교 값: 버전, 계정 문맥, symbol, side, type, timeInForce, 정규화한 가격·수량 또는 취소 대상 ID. 네트워크 연결·재시도 시각은 비교하지 않는다.
- 해시만으로 동등하다고 판단하지 않고 정규화된 필드도 비교한다. 클라이언트 시각 필드는 v1에서 받지 않는다. 순서·체결 ID·최초 결과에 벽시각이 필요하지 않다.
- 동일 pending 요청은 기존 처리에 연결한다. 실험 명령 한도 도달 여부보다 완료/pending 중복 확인을 먼저 한다. 한도 도달 후에도 최초 결과·조회는 제공한다.

## 4. 변경 API

| 메서드·경로 | 필수 본문 필드 | 결과 |
| --- | --- | --- |
| POST `/v1/orders` | clientOrderId, symbol=`BTC-USDT`, side=`BUY` 또는 `SELL`, type=`LIMIT`, timeInForce=`GTC`, priceTicks, quantityLots | 최초 주문 처리 결과 |
| POST `/v1/cancellations` | cancelRequestId, clientOrderId | 최초 취소 처리 결과 |

주문 예시:

```http
POST /v1/orders HTTP/1.1
Host: 127.0.0.1:8080
Content-Type: application/json
X-Run-ID: 11111111-1111-4111-8111-111111111111
X-Account-ID: alice

{"clientOrderId":"buy-1","symbol":"BTC-USDT","side":"BUY","type":"LIMIT","timeInForce":"GTC","priceTicks":"6000000","quantityLots":"100"}
```

최초 결과는 HTTP 200이다. 업무 거절도 로그 저장·적용이 끝난 최종 결과이므로 같은 HTTP 200으로 반환하고 code로 구분한다. 아래 예시는 상대 매도 주문이 없는 경우다.

```json
{
  "schemaVersion": 1,
  "runId": "11111111-1111-4111-8111-111111111111",
  "accountId": "alice",
  "kind": "PLACE_ORDER",
  "requestId": "buy-1",
  "commandSeq": "1",
  "code": "ACCEPTED",
  "order": {
    "clientOrderId": "buy-1", "symbol": "BTC-USDT", "side": "BUY",
    "priceTicks": "6000000", "originalQty": "100", "executedQty": "0",
    "openQty": "100", "cancelledQty": "0", "executedQuoteAtoms": "0",
    "status": "OPEN", "reason": null, "createdSeq": "1", "updatedSeq": "1"
  },
  "tradeCount": "0"
}
```

`requestId`는 각 종류의 clientOrderId/cancelRequestId다. `order`는 명령 적용 직후의 불변 사본이며 현재 주문 객체 참조가 아니다. `tradeCount`는 이번 명령이 만든 체결 개수이고 취소는 0이다. 체결 상세 배열은 응답 크기를 제한하기 위해 별도 조회한다. 최초 body에는 응답 시각·캐시 적중 여부를 넣지 않아 재생과 중복 응답이 같은 값을 갖는다. HTTP Date 등의 전송 메타데이터는 이 계약 밖이다.

| kind | code | order |
| --- | --- | --- |
| PLACE_ORDER | ACCEPTED | OPEN/PARTIALLY_FILLED/FILLED/CANCELLED. STP 잔량 취소는 reason=SELF_TRADE_PREVENTION |
| PLACE_ORDER | INSUFFICIENT_BALANCE | REJECTED 사본. originalQty는 입력, 나머지 수량·대금은 0, reason=INSUFFICIENT_BALANCE |
| CANCEL_ORDER | CANCELLED | 잔량 취소 후 사본, reason=USER_CANCEL |
| CANCEL_ORDER | ALREADY_FILLED / ALREADY_CANCELLED | 조회 시점에 존재한 종료 주문 사본, 변경 없음 |
| CANCEL_ORDER | ORDER_NOT_FOUND | null. 존재하지 않음·다른 계정·REJECTED 주문은 동일하게 처리 |

취소 예시: `{"cancelRequestId":"cancel-1","clientOrderId":"buy-1"}`. 같은 ID의 취소 재시도는 나중 상태와 관계없이 원래 결과를 반환한다. 생성 재시도도 최초 OPEN 결과를 유지하며 현재 FILLED 여부는 조회로 확인한다.

## 5. 실패와 결과 미확정

최초 업무 결과가 아닌 응답은 `{"error":{"code":"QUEUE_FULL","outcome":"NOT_ADMITTED"}}`처럼 반환한다. `NOT_ADMITTED`는 **이번 시도**를 새 명령으로 접수하지 않았다는 뜻이다. 같은 키의 과거 시도 여부는 단정하지 않는다. `UNKNOWN`이면 저장·적용됐을 가능성이 있어 같은 ID로 조회/재시도한다. 실패 응답은 최초 업무 결과 캐시에 저장하지 않는다.

| HTTP | code | outcome |
| --- | --- | --- |
| 400 | INVALID_REQUEST (필드·단위·범위·잘못된 JSON) | NOT_ADMITTED |
| 404 / 405 | ROUTE_NOT_FOUND / METHOD_NOT_ALLOWED (405에는 Allow 헤더) | NOT_ADMITTED |
| 403 | UNKNOWN_ACCOUNT / ORIGIN_FORBIDDEN / HOST_FORBIDDEN | NOT_ADMITTED |
| 409 | RUN_MISMATCH / IDEMPOTENCY_CONFLICT / RUN_LIMIT_REACHED | NOT_ADMITTED |
| 413 / 415 | BODY_TOO_LARGE / UNSUPPORTED_MEDIA_TYPE | NOT_ADMITTED |
| 429 | QUEUE_FULL / TOO_MANY_REQUESTS / DUPLICATE_WAITERS_FULL | NOT_ADMITTED |
| 503 | NOT_READY (접수 전) | NOT_ADMITTED |
| 503 | STORAGE_UNAVAILABLE / INTERNAL_FAILURE (접수 후) | UNKNOWN, fail-stop |
| 504 | RESULT_TIMEOUT (접수 후) | UNKNOWN |
| 408 | REQUEST_TIMEOUT (본문 읽기·접수 전 context 만료) | NOT_ADMITTED |
| 505 | PROTOCOL_UNSUPPORTED (HTTP/1.1 이외) | NOT_ADMITTED |

형식/계정/runId/범위 검사는 WAL 이전, 가용 잔고·취소 대상 상태 검사는 저장 후 순차 적용 단계다. HTTP 파서가 handler 호출 전에 거절하는 잘못된 프레임·중복 Host·헤더 초과(431), 헤더 읽기 timeout은 JSON 오류 형식을 보장하지 않는다. 본문 검사 중 timeout·응답 연결 단절처럼 JSON 오류 body를 전달할 수 없는 경우도 있으므로 클라이언트는 네트워크 실패 자체를 확정 업무 거절로 바꾸지 않는다. 내부 원인·파일 경로 등 진단은 오류 body에 포함하지 않는다.

## 6. 조회 API와 일관성

조회는 해당 계정 문맥으로만 자기 주문·잔고·명령 결과에 접근한다. 호가와 체결 가격·수량은 실험 공통 데이터다. 업무 상태는 한 명령의 전체 적용이 끝난 경계에서 복사하고 `runId`, 문자열 `asOfSeq`를 반환한다. 조회끼리의 동일 시점을 보장하지 않으며 응답 도착 순서가 commandSeq 순서를 뒤집을 수 있다.

| 경로 | 결과 |
| --- | --- |
| GET `/v1/orders/{clientOrderId}` | 현재 Order 사본. REJECTED도 조회 가능. 자기 계정에 없으면 404 ORDER_NOT_FOUND |
| GET `/v1/balances` | BTC의 availableLots/reservedLots, USDT의 availableAtoms/reservedAtoms |
| GET `/v1/book?limit=20` | 가격별 합산 quantityLots의 bids 내림차순, asks 오름차순; 각 면 limit개, 1~100 |
| GET `/v1/trades?limit=100` | tradeId, priceTicks, quantityLots, takerSide. `(commandSeq, matchIndex)` 오름차순; limit 1~1,000 |
| GET `/v1/results/place/{clientOrderId}` | 완료 시 HTTP 200과 최초 body 그대로 |
| GET `/v1/results/cancel/{cancelRequestId}` | 완료 시 HTTP 200과 최초 body 그대로 |
| GET `/health/live` | 프로세스 응답 여부, 계정 문맥 불필요 |
| GET `/health/ready` | 복구·동기화 완료 시 200, 나머지 503. 거래 가능성과 live를 구분 |

결과 조회에서 owner가 pending을 확인하면 HTTP 202 `{"state":"PENDING"}`, 아직 키를 확인할 수 없으면 404 `{"state":"UNKNOWN"}`이다. 404는 미처리의 증명이 아니다. 큐에서 아직 owner로 이동하지 않은 요청·전달 지연을 고려해 같은 ID로 재시도할 수 있다. 완료 결과에는 현재 asOfSeq를 추가하지 않는다. 원래 commandSeq가 그 결과의 적용 경계다.

체결 페이지는 첫 조회의 asOfSeq를 `throughSeq`로 고정하고 다음 요청에 전달한다. `afterSeq`, `afterIndex`는 마지막 체결의 위치이며 둘 다 있어야 한다. 최초에는 생략한다. 이후 페이지는 `commandSeq <= throughSeq` 안에서만 조회하여 새 체결 유입으로 페이지 경계가 바뀌지 않는다. tradeId는 `runId:commandSeq:matchIndex`, matchIndex는 명령 내 1부터 시작한다. v1은 체결을 삭제하지 않는다. 잘못된 cursor나 현재 appliedSeq보다 큰 throughSeq는 400이다.

조회 wire DTO는 [api response 타입](../../internal/api/response.go)에 고정한다. 정수는 모두 문자열이며 빈 bids/asks/trades는 `[]`다. 미지·중복 query parameter, 빈 숫자·부호·소수·지수는 400이다. limit을 생략하면 book은 20, trades는 100이다.

| 조회 | JSON 필드 |
| --- | --- |
| 현재 주문 | runId, asOfSeq, order (전체 Order) |
| 잔고 | runId, asOfSeq, BTC {availableLots, reservedLots}, USDT {availableAtoms, reservedAtoms} |
| 호가 | runId, asOfSeq, bids/asks [{priceTicks, quantityLots}] |
| 체결 | runId, asOfSeq, throughSeq, trades [{tradeId, priceTicks, quantityLots, takerSide}], nextCursor |

nextCursor는 페이지가 limit에 도달하면 마지막 체결의 `{afterSeq, afterIndex}`이며, 나머지는 null이다. 마지막 페이지의 크기가 limit과 같으면 다음 요청이 빈 배열을 반환할 수 있다. 다음 요청에는 cursor의 양수 두 값과 응답의 throughSeq를 함께 보낸다. 첫 페이지에 cursor를 쓰지 않으며 `0/0` cursor는 거절한다. public 체결에는 maker/taker의 계정·주문 ID를 노출하지 않는다. health 성공 body는 각각 `{"state":"LIVE"}`, `{"state":"READY"}`이며 ready 실패는 NOT_READY 오류다. 모든 handler JSON 응답은 `Cache-Control: no-store`다.

## 7. 검증 연결

P1은 단위·정수 overflow·초기 공급량·잔고 계산을 검증한다. P3/P5는 엄격한 JSON·계정 문맥·큐 한도·중복·UNKNOWN·불변 최초 응답·조회 경계·페이지 고정 시점에 테스트를 붙인다. 특히 `"000100"` 재시도, 같은 키의 다른 가격, JSON 중복 키/키 대소문자, float/지수 입력, 4,097-byte 본문, 명령 한도 뒤 재시도, 접수 직후 disconnect를 포함한다.

P5의 [handler 검사](../../internal/api/server_test.go), [실제 소켓 경계](../../internal/api/network_test.go), [APFS/runtime 통합](../../internal/api/native_test.go)이 구현되어 있다. HTTP 전역 한도·오류 매핑과 P3 runtime의 큐/저장 오류 검증을 구분한다. HTTP 요청부터 큐 포화·저장 실패·프로세스 종료까지의 추가 장애 행렬, 서버 CLI와 실제 전원 장애 검증은 남아 있다.
