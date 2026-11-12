# 수동 거래와 같은 ID 재시도 CLI

상태: Completed
종료일: 2026-11-12

## Purpose / Big Picture

개발자가 `cmd/trade`를 통해 계정·표시 가격·BTC 수량을 지정해 주문하고 취소하며, 잔고·호가·체결·주문·최초 결과를 조회한다. 타임아웃을 업무 거절로 해석하지 않고 전송 전에 보존한 요청으로 같은 ID를 조회·재시도한다. 서버·저장·거래 규칙과 초기 자금 정책은 유지한다. 전원 장애·새 성능 측정은 이번 구현 범위 밖이다.

## Progress

- [x] 기존 API·정수·중복/UNKNOWN 계약 확인 및 feature branch 생성.
- [x] 명령·정수 변환·loopback 전송·요청 파일/결과 조회/재시도 구현.
- [x] 오류/타임아웃/응답 유실/재시도와 실제 거래·재시작 검증.
- [x] 문서·CI 실행 확인 갱신과 전체 검증.

## Surprises & Discoveries

결과 조회의 202 PENDING과 404 UNKNOWN은 완료 거절이 아니다. 최초 응답과 현재 주문 상태도 다르므로 조회 명령을 분리한다. 기존 domain의 정규화·검증과 API DTO를 재사용하며 engine/runtime을 직접 호출하지 않는다.

## Decision Log

- 2026-11-12: 사용자가 변경 검토·커밋·마스터 병합/푸시와 GitHub CI 확인을 요청했다. 입력·요청 보존·UNKNOWN/충돌·서버 재시작 경계를 검토했고 추가 수정이 필요한 문제는 발견하지 않았다. 대상 uncached race를 다시 통과했다. 원격 상태는 push 뒤 마스터의 실제 커밋으로 확인한다.
- 2026-11-12: 명령 첫 인자는 subcommand다. 가격은 USDT/BTC 소수 둘째 자리, 수량은 BTC 소수 넷째 자리까지 정확히 받고 반올림하지 않는다. 요청 ID는 사용자가 필수 지정한다.
- 2026-11-12: 변경 요청은 새 0600 요청 파일을 기록·sync/close한 뒤 한 번 전송한다. 재시도는 파일의 URL/run/account/종류/본문을 고정한다. 결과 조회 200도 원래 취소 대상 등 입력 전체의 동등성을 증명하지 못하므로 명시적 retry는 200/PENDING/UNKNOWN 조회 후 같은 본문을 POST하여 서버의 충돌 검사를 거친다. result 명령은 GET만 수행한다. 조회 자체가 실패하면 재전송하지 않는다.
- 2026-11-12: URL은 DNS/프록시/redirect 없이 숫자 loopback HTTP만 허용한다. stdout은 JSON, 진단·저장 경로는 stderr다. 새 production dependency는 없다.

## Outcomes & Retrospective

8개 명령과 표시 단위의 정확한 정수 변환, 전송 전 요청 파일·기존 파일 재사용 거절·같은 ID 결과 확인/명시적 재시도를 구현했다. 완료된 결과 조회만으로 다른 취소 내용을 숨기지 않도록 재시도 POST의 서버 충돌 검사를 유지한다. 실제 서버 재시작 뒤 최초 결과가 같고 중복 요청이 순번을 늘리지 않음을 확인했다. 서버·매칭·WAL 계약과 production dependency는 변경하지 않았다. 커밋·병합·push 상태는 Git 기록, 원격 CI는 해당 커밋의 Actions 실행 결과를 확인한다. 실제 전원 장애 검증은 계속 보류다.

## Validation and Acceptance

정수 경계·잘못된 입력·파일 재사용/손상·응답 identity·HTTP 오류·PENDING/UNKNOWN·응답 유실 뒤 같은 본문 재전송을 테스트한다. native 통합은 실제 HTTP 주문/부분 체결/취소·조회/페이지와 서버 재시작 후 같은 최초 결과를 확인한다. portable/native 전체 CI와 문서 경로/공백을 검사한다. 실제 전원 장애 검증은 계속 보류한다.

실제 결과:

- 대상 race: `scripts/go test -race -count=1 ./cmd/trade ./tools/civerify -timeout=180s` 통과(`var/trading-cli-targeted.log`).
- 발행 전 검토의 같은 대상 uncached race 재실행도 통과했다(`var/trading-cli-review-targeted.log`).
- 첫 portable CI는 테스트의 불필요한 append를 vet이 지적해 실패했다(`var/trading-cli-ci-portable.log`, `var/ci/portable.msdmPf`). 수정 후 전체를 다시 검증했다.
- 최종 portable: 215개·21 package, Python 32개·진단 smoke·포맷/vet/build 통과(`var/trading-cli-ci-portable-final.log`, `var/ci/portable.zIENDz`).
- 최종 native race: 249개·21 package, 명시된 child-helper skip 1개, Python 32개·실제 APFS/HTTP/복구 진단 smoke·포맷/vet/build 통과(`var/trading-cli-ci-native.log`, `var/ci/native.2CrjZ3`). 최종 native에는 완료된 ORDER_NOT_FOUND 조회 뒤 다른 취소 대상의 재시도를 충돌로 거절하는 검사도 포함한다.
- 실제 `bin/trade` 프로세스 smoke: `var/trading-cli-smoke-aougmrh8/summary.json` PASSED. 주문/부분 체결/잔량 취소·예약금 반환·잔고/호가/체결/주문 조회, 실제 서버 재시작 뒤 최초 결과·주문/취소 재시도·순번 3 불변과 UNKNOWN 조회 종료 코드 1을 확인했다.
- 새/변경 Markdown의 로컬 링크·포맷·공백 검사를 수행했다. 성능 측정·장비 전원 제어·외부 발행은 실행하지 않았다.
