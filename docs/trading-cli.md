# 거래용 CLI

`cmd/trade`는 로컬 모의 거래소의 공개 HTTP API로 직접 주문·취소·조회한다. 초기 설정에 등록된 테스트 계정을 선택하며 실제 자산·실입출금·실서비스 인증을 제공하지 않는다. 서버의 거래·저장·복구 계약은 변경하지 않는다.

## 준비와 첫 주문

서버는 macOS arm64/APFS/cgo 환경을 사용한다. CLI의 전송/입력 검사는 portable CI에서도 실행한다. 저장소 루트에서 빌드한다.

```sh
sh scripts/setup-go.sh
mkdir -p bin var/manual-trading
scripts/go build -mod=readonly -o bin/ ./cmd/exchange ./cmd/trade
bin/exchange init --config config/examples/local.json --data-dir var/manual-trading/run
bin/exchange serve --data-dir var/manual-trading/run --listen 127.0.0.1:8080 > var/manual-trading/ready.json
```

마지막 명령은 서버를 계속 실행한다. 다른 터미널에서 ready 파일이 생성된 뒤 실행한다. `init`은 새 실험을 만들 때 한 번만 사용한다. 일반 재시작에는 같은 `serve`만 실행하며 기존 잔고를 복구한다. ready 파일은 매 기동 때 새 보고서로 갱신한다.

```sh
bin/trade place --server-report var/manual-trading/ready.json --account alice \
  --id buy-1 --side BUY --price 60000 --quantity 0.01 \
  --request var/manual-trading/buy-1.json

bin/trade place --server-report var/manual-trading/ready.json --account bob \
  --id sell-1 --side SELL --price 59900 --quantity 0.004 \
  --request var/manual-trading/sell-1.json

bin/trade order --server-report var/manual-trading/ready.json --account alice --id buy-1
bin/trade balances --server-report var/manual-trading/ready.json --account alice
bin/trade book --server-report var/manual-trading/ready.json --account alice --limit 20
bin/trade trades --server-report var/manual-trading/ready.json --account alice --limit 100

bin/trade cancel --server-report var/manual-trading/ready.json --account alice \
  --id cancel-1 --order-id buy-1 --request var/manual-trading/cancel-1.json
```

예시는 초기 호가가 비어 있을 때 Alice의 매수 0.01 BTC 중 0.004 BTC가 대기 가격 60,000 USDT/BTC에 체결되고 0.006 BTC를 취소한다. 취소 전 예약금은 360 USDT, 취소 후 0이다. 기본 local 설정은 alice/bob/carol 각각 1 BTC·100,000 USDT다. 같은 ID를 새 거래에 재사용하지 않는다.

## 명령과 단위

첫 인자는 명령이고 나머지는 해당 명령의 flag다. `bin/trade -h`, `bin/trade place -h` 등으로 도움말을 확인한다.

| 명령 | 고유 옵션과 의미 |
| --- | --- |
| `place` | `--id`, `--side BUY|SELL`, `--price`(USDT/BTC), `--quantity`(BTC) 필수. 지정가 GTC·BTC-USDT 고정 |
| `cancel` | `--id`(취소 요청 ID), `--order-id`(취소할 주문 ID) 필수 |
| `balances` | 선택 계정의 BTC/USDT 가용·예약 잔고 |
| `book` | 가격별 호가, `--limit 1..100`, 기본 20 |
| `trades` | 체결 페이지, `--limit 1..1000`, 기본 100 |
| `order` | `--id`로 현재 주문 상태 조회 |
| `result` | `--kind place|cancel --id`로 최초 결과 조회, 또는 `--request`로 저장한 문맥 사용. GET만 전송 |
| `retry` | `--request` 필수. 저장한 문맥으로 결과 조회 후 같은 본문 POST |

일반 명령은 `--server-report <ready 파일> --account <계정>`을 받는다. 보고서 대신 `--url http://127.0.0.1:8080 --run-id <서버의 runId> --account <계정>`을 직접 지정할 수도 있다. 보고서와 URL/runId를 동시에 지정할 수 없다. URL은 숫자 loopback HTTP·명시적 port만 허용하고 DNS·프록시·redirect는 사용하지 않는다. `result --request`와 `retry --request`는 URL/runId/계정을 포함한 문맥을 파일에서 읽으며 덮어쓰기 flag를 거절한다.

가격은 소수 둘째 자리, BTC 수량은 소수 넷째 자리까지 허용한다. 예를 들어 `60000.01`, `0.0001`을 정확히 변환한다. float나 반올림을 사용하지 않으며 초과 자릿수·부호·지수·0·범위 초과를 전송 전에 거절한다. 가격 한도 1,000,000 USDT/BTC, 수량 한도 100 BTC와 대금 한도는 [API 계약](design-docs/api-v1.md)을 따른다. 출력은 API의 정수 문자열 단위다(`priceTicks / 100` USDT/BTC, `quantityLots / 10000` BTC, `quoteAtoms / 1000000` USDT).

체결 다음 페이지는 응답의 `throughSeq`를 유지하고 `nextCursor.afterSeq`, `afterIndex`를 각각 `--through-seq`, `--after-seq`, `--after-index`로 지정한다. cursor 두 값은 양수이며 모두 함께 필요하다. 마지막 페이지 크기가 limit과 같으면 다음 페이지가 빈 배열일 수 있다. CLI는 한 페이지씩 조회해 응답을 제한하며 실행 중 새 체결 때문에 과거 페이지 범위를 바꾸지 않는다.

## 타임아웃과 같은 ID 재시도

주문·취소는 사용자가 ID를 지정한다. 전송 전에 URL·runId·계정·종류·정규화한 본문을 새 0600 파일에 기록하고 file sync/close가 성공해야 POST를 보낸다. `--request`를 생략하면 `var/requests/request-<고유값>.json`에 저장하고 경로를 stderr로 출력한다. 지정 parent는 존재해야 하며 기존 파일은 덮어쓰지 않는다. 저장·경로 출력 실패 시 요청을 보내지 않는다. 이 파일은 서버 WAL이나 전원 장애용 내구성 증거가 아니라 클라이언트의 재시도 입력이다.

요청별 timeout은 기본 5초, `--timeout`으로 0 초과~1분까지 설정한다. 네트워크 timeout·연결 종료·잘린/잘못된 결과는 처리 여부가 **UNKNOWN**이다. 자동 재시도나 새 ID 생성은 하지 않는다.

```sh
bin/trade result --request var/manual-trading/buy-1.json
bin/trade retry --request var/manual-trading/buy-1.json --timeout 10s
bin/trade result --request var/manual-trading/cancel-1.json
bin/trade retry --request var/manual-trading/cancel-1.json --timeout 10s
```

`result`는 최초 응답을 조회한다. `202 PENDING`과 `404 UNKNOWN`은 미완료·미확정이며 미처리의 증명이 아니다. `retry`는 먼저 결과를 조회하고, 완료/PENDING/UNKNOWN이면 저장한 같은 ID·내용을 한 번 POST한다. 완료 결과 조회도 원래 입력 전체를 노출하지 않으므로 POST로 서버의 중복 내용 비교를 거친다. 기존 결과가 있으면 최초 응답을 반환하고, 다른 내용이면 409 IDEMPOTENCY_CONFLICT로 실패한다. 조회의 네트워크 실패·다른 HTTP 오류·identity 불일치에서는 POST를 보내지 않는다. 파일을 수정하거나 다른 run/계정으로 옮겨 재시도하지 않는다.

서버 재시작 뒤에는 같은 포트·데이터를 사용하고 저장한 요청 파일을 유지한다. 새 실험의 다른 runId 서버라면 RUN_MISMATCH로 거절된다. 현재 주문은 이미 체결/취소되었어도 `result`와 정상 재시도의 최초 응답은 과거 OPEN일 수 있다. 현재 상태는 `order`로 별도 확인한다.

stdout은 JSON 결과, stderr는 진단과 요청 파일 경로다. HTTP 200의 업무 거절(`INSUFFICIENT_BALANCE`, `ORDER_NOT_FOUND` 등)도 확정 결과로 출력하고 종료 코드 0이며 `code`를 확인해야 한다. HTTP 오류·통신/입력 오류·아직 최종 결과가 아닌 조회는 종료 코드 1이다. NOT_ADMITTED는 이번 시도만의 결과이며 같은 ID의 과거 시도 여부까지 보장하지 않는다.

## 검증

```sh
scripts/go test -race ./cmd/trade ./tools/civerify -timeout=180s
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

일반 테스트는 정확한 단위/한도·요청 파일 검증과 덮어쓰기 거절, 전송 전 저장, timeout/응답 유실 후 같은 내용 재전송, pending/unknown·충돌·identity 오류·잘못된/과대 응답·redirect 차단을 확인한다. native 테스트는 실제 서버 프로세스에서 거래/부분 체결/잔량 취소·잔고/호가/체결 페이지·업무 거절과 정상 재시작 뒤 최초 결과·중복 순번 불변을 검증한다. 성능 측정·실제 전원 장애 검증은 수행하지 않는다.

2026-11-12 로컬 전체 CI는 portable 215개·native race 249개(명시된 child-helper skip 1개), 각 Python 32개와 진단 smoke까지 통과했다. 실제 `bin/trade`와 서버 프로세스를 통한 주문·조회·취소·재시작·중복 순번 불변도 확인했다. 증거와 최초 vet 실패/수정 이력은 [완료 계획](exec-plans/completed/20261112-trading-cli.md)에 기록했다. 원격 GitHub CI는 push 뒤 해당 커밋의 실제 실행 결과로 확인한다.
