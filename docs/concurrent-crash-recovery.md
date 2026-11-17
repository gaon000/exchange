# 동시 요청 중 SIGKILL·복구 검증

4개 HTTP 클라이언트의 주문·취소·같은 ID 요청이 진행 중인 **테스트용 자식 서버**를 세 저장 경계에서 SIGKILL한다. 실제 WAL 입력/순서로 잔고·예약금·주문·호가/FIFO·체결·최초 응답과 재시도 중복 방지를 검사한다. 기존 [정상 재시작 동시 검사](concurrent-recovery.md)와 별도 명세/자료를 사용한다. 성능 측정이 아니며 실제 OS·전원 장애 검증은 미완료·보류다.

## 실행·재현

[지원 환경](development.md)의 macOS/APFS/cgo·Go와 loopback 권한이 필요하다. 추가 dependency는 없다.

```sh
# 세 경계를 각 새 거래 상태에서 실행
sh scripts/concurrent-crash.sh --seed 17 --output var/crash-example

# 실제 저장 순서·복구 뒤 신규 접수 순서를 포함한 명세를 새 폴더에서 실행
sh scripts/concurrent-crash.sh --replay var/crash-example/replay-005.json --output var/crash-example-replay
```

output은 존재하지 않아야 하며 모든 parent가 기존 실제 디렉터리여야 한다. 기존 자료/거래 디렉터리 안쪽·symlink parent·원본 replay 자료 안쪽은 거절한다. 생략하면 `var/crash-scenarios/<UTC 시각>`을 쓴다. seed는0~2147483647이고 replay와 동시에 지정할 수 없다. 기존 `scripts/random-recovery.sh`의 v1/v2 명세와 새 명세를 섞지 않는다. 입력은1MiB 이하 일반 파일이며 알 수 없는 필드·중복 키·변경된 fixture/경계·중복/잘못된 기록 순서는 실행 전에 거절한다.

실행기는 `scripts/go test -c`로 `bin/concurrent-crash.test`를 만들고 `TestConcurrentCrashCommand`만 선택한다. 기존 `HTTPTestHooks`를 재사용하며 장애 제어/HTTP endpoint를 일반 서버에 추가하지 않는다. 자식은 같은 test binary에서 실제 runtime·API·WAL/snapshot을 사용한다. 단순 mock 서버나 WAL bytes 주입이 아니다. 테스트 프로세스/실행기이지 배포용 `bin/exchange` 바이너리는 아니다. script를 다시 실행하면 현재 소스로 재빌드하며 과거 바이너리가 자동 복원되는 것은 아니다.

## 세 경계

각 case는 초기 계정3개·고유 명령 최대6개인 제한된 실험이다. 초기 매도8 lots와 부분 매수1 lot의 성공 응답·snapshot seq2를 먼저 보존한다. 다음 네 HTTP 요청이 모두 서버의 시험 gate에 도착한 뒤 함께 연다: bob의 동일 매수 두 복사본, alice의 잔량 취소, carol의 대기 매수. 매수 수량은 seed가 선택한다. 신규 WAL 키의 순서는 자유 실행에서 제출 배열/클라이언트 완료 순서와 다를 수 있다.

| 경계 | 실제 종료 지점 | 복구에서 기대하는 선택 |
| --- | --- | --- |
| `wal-after-sync` | 기존 Store.Append가 실제 기록·엄격한 동기화를 성공한 직후 hook에서 멈춤. worker가 완료를 owner에 알리기 전이므로 해당 명령은 아직 적용/응답되지 않음 | snapshot2 + WAL tail1. 다른 두 신규 키는 이 실험의 WAL에 없음 |
| `snapshot-before-publish` | 네 요청의 고유3명령 적용을 기다리고 결과 응답은 보류. seq5 사본의 Publish를 호출하기 직전 멈춤 | 이전 snapshot2 + WAL tail4 |
| `snapshot-after-publish` | seq5를 실제 Publish하여 파일/디렉터리 동기화까지 성공한 뒤 worker가 owner에 완료를 알리기 전 멈춤 | 새 snapshot5 + WAL tail1 |

snapshot 두 경계에서는 worker를 멈춘 동안 별도 후속 매도1 lot를 제출하여 성공 응답과 seq6을 확인한 뒤 죽인다. 이 후속 명령은 snapshot 이후 WAL로 복구돼야 한다. 초기 네 요청은 결과가 적용됐더라도 HTTP 응답을 반환하지 않도록 시험 backend에서 보류한다. 죽이기 직전 네 호출이 아직 진행 중인지 확인하고 `Process.Kill`은 직접 시작한 자식 객체에만 호출한다. WaitStatus가 실제 SIGKILL인지 확인한다. 다른 서버/사용자 앱/OS는 종료하지 않는다.

## 복구·최초 응답 계약

1. 자식 종료와 네 클라이언트 합류 후 실제 WAL을 소유 잠금 아래 읽는다. 이전 prefix 보존, 연속 순번·허용 입력·고유 키 한 번 기록을 검사한다. 클라이언트 완료 순서를 모델의 입력 순서로 쓰지 않는다.
2. WAL 순서의 순차 엔진으로 전체 상태와 최초 결과를 만든다. 종료 전 성공 응답한 초기/후속 명령이 모두 존재하고 최초 응답 bytes도 같아야 한다. 엔진은 서버와 같은 구현이며 독립 매칭 oracle을 새로 만든 것은 아니다.
3. 실제 Runtime.Open의 readiness 이후 선택 snapshot·tail 건수와 공개 HTTP의 잔고·현재 주문·호가·체결·최초 결과를 대조한다. 네 원래 시도의 Outcome은 **UNKNOWN 그대로** 남는다. 후속 GET에서 저장된 결과를 찾거나404 UNKNOWN을 보더라도 원래 관측을 성공/미접수로 바꾸지 않는다.
4. snapshot 전체 checkpoint를 WAL 모델과 비교하여 FIFO·예약금·공급량·최초 결과를 확인한다. 이 검증을 위해 새 snapshot을 만드는 시점과 장애 당시 선택 snapshot을 구분한다. 조회/확인용 snapshot은 WAL을 늘리지 않아야 한다.
5. 같은 ID·내용 네 요청을 다시 동시에 제출한다. 이미 저장된 키는 최초 결과를 반환하고, 복구 후 미확정인 키는 같은 ID로 처음 저장될 수 있다. 전체 WAL을 다시 읽어 고유 키가 정확히 한 번씩 존재하고 복구 전 prefix/최초 결과가 유지되는지 확인한다. 실제 추가 순서로 모델을 갱신해 응답과 전체 checkpoint를 대조한다.
6. 다시 정상 재기동하고 모든 최초 결과를 GET으로 확인한 뒤 고유 ID 전부를 재시도한다. WAL bytes와 체결/잔고 상태가 늘거나 바뀌면 실패다.

이 실험에서 WAL에 없는 키를 알 수 있는 것은 소유 프로세스 종료·저장소 검사라는 별도 증거가 있기 때문이다. 일반 API의404 UNKNOWN을 원래 요청의 미접수 보장으로 해석하지 않는다. **404는 계속 미확정이며 재시도는 같은 ID/내용을 사용한다.**

## 재현과 보존 파일

전체 제한은120초, case는30초이며 test runner는150초다. 취소·실패에도 자식을 종료/reap하고 HTTP 호출을 합류한 뒤 실패를 기록한다. 원본은 지우거나 재사용하지 않는다. 프로세스/파일 정리가 끝나기 전에 completed를 게시하지 않는다.

`scenario.json`은 seed·세 경계·전체 fixture 입력이다. 관측한 pre-kill WAL 순서를 얻으면 새 `replay-NNN.json`을 쓰고 재시도 후 추가 순서를 얻으면 다시 쓴다. 완전한 실행의 마지막 파일은 `replay-005.json`이다. 기록 replay에서는 새 고유 키의 다음 제출을 이전 키의 upstream 완료 뒤에 열어 관측 순서를 재현하며, 같은 ID 복사본은 병행할 수 있다. runId·PID·포트·시간/OS 스케줄의 동일성을 뜻하지 않는다.

출력을 만든 뒤 실행 중 실패하면 failed.json과 표준 출력에 `sh scripts/concurrent-crash.sh --replay '...'` 명령이 남는다. 잘못된 입력·기존 출력·지원하지 않는 저장 환경은 새 출력 생성 전에 거절한다. 명령의 output을 생략하면 새 자동 폴더를 사용한다. 마지막 유효한 기록 명세를 선택하며 부분/손상 JSON은 무시한다. 관측 순서를 아직 확보하지 못했으면 원래 입력/경계 또는 마지막 유효 명세를 재실행한다. 미관측 interleaving·디스크/환경 오류 자체의 동일 발생은 보장하지 않는다.

| 파일 | 내용 |
| --- | --- |
| `scenario.json`, `config.json`, `build.json` | 입력·초기 설정·Go/플랫폼 및 실제 test binary hash |
| `replay-*.json` | 관측한 pre-kill 순서와 복구 후 추가 순서 |
| `<경계>/case.json`, `child.stdout.jsonl`, `child.stderr.log` | 자식 입력과 ready/네 요청 도착/응답 보류/저장 경계·오류 |
| `kill-boundary.json`, `kill-confirmed.json`, `acknowledged.json` | 죽이기 전 네 호출 진행 중·PID/경계와 실제 신호 확인, 이미 받은 성공 응답 |
| `initial-observations.json` | 클라이언트별 원래 UNKNOWN·전송 오류·지연. 후속 성공으로 덮어쓰지 않음 |
| `crash-wal.json`, `recovery.json`, `followup-results.json` | 실제 저장 입력/순번·선택 snapshot/tail·복구 뒤 첫 GET 관측 |
| `retry-responses.json`, `final-wal.json` | 같은 ID 동시 재시도 응답과 이후 실제 저장 순서 |
| `recovered-*`, `final-*`, `second-recovery-http.json`, `run/` | 전체 checkpoint/HTTP 대조·재기동 후 최초 결과·실제 거래 저장소 |
| `summary.json`, `manifest.json`, `completed.json`, `failed.json` | 결과/건수·모든 종료 뒤 파일 봉인 또는 실패 재현 명령 |

기존 v1 전송 오류 보고서 입력으로 섞지 않으며 새 도구의 JSON/원본 증거를 사용한다. [실제 결과](benchmarks/concurrent-crash-recovery.md)를 함께 읽는다.

## 자동 검증

```sh
scripts/go test -race ./internal/runtime -run 'TestConcurrentCrash|TestNativeConcurrentCrash' -count=1 -timeout=180s
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

일반 CI는 CLI/명세·입력 보호를 검사한다. native는 seed17/42의 세 경계와 기록 replay, 출력 실패 뒤 재현, 네 요청 진행 중 취소/reap·잠금 해제, 잘못된 WAL 순서/키 회귀를 검사한다. 성공·실패 fixture는 기존 `var/ci/*/random-recovery-*` artifact 경로에 보존한다. portable에서는 저장 미지원이 새 출력 생성 전에 실패해야 한다. 실제 전원 장애 검증은 계속 미완료·보류다.
