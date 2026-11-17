# 동시 요청·장애 조합 통합 검증

`tools/recoverycheck --concurrent`는 실제 loopback HTTP 클라이언트4개에서 주문·취소·같은 ID 재시도와 응답 유실·snapshot·정상 재시작을 조합한다. 실제 WAL의 명령/순번으로 잔고·공급량·주문·호가/FIFO·체결·최초 응답과 중복 방지를 검사한다. 성능 측정이 아니며 실제 OS·전원 장애 검증은 미완료·보류다.

동시 요청 중 실제 SIGKILL 경계는 별도 [프로세스 강제 종료 실행기](concurrent-crash-recovery.md)를 사용한다. 일반 서버에 장애 옵션을 추가하지 않고 runtime test binary와 기존 test-only hook으로 실행한다.

## 한 명령 실행·재현

[지원 환경](development.md)의 Go·macOS arm64/APFS/cgo와 loopback 권한이 필요하다. 추가 dependency는 없다.

```sh
# seed17, 4개 클라이언트·두 wave
sh scripts/random-recovery.sh --concurrent --seed 17 --output var/concurrent-example

# 완료한 wave의 실제 저장 순서/스냅샷 경계를 포함해 새 폴더로 재실행
sh scripts/random-recovery.sh --replay var/concurrent-example/replay-005.json

# 관측 순서를 고정하지 않고 원래 동시 입력·gate 계획을 다시 실행
sh scripts/random-recovery.sh --replay var/concurrent-example/scenario.json
```

자동 출력은 `var/scenarios/<UTC 시각>`이며 parent·원본/명세·symlink·기존 출력 보호는 [공통 실행 규칙](random-recovery.md#한-명령-실행재현)을 따른다. seed는0~2147483647이다. concurrent는 actions/transport-loss/replay와 함께 지정하지 않는다. replay는 생성기 flag와 함께 지정하지 않는다. 기존 v1 seed·transport-loss 명세는 유지한다.

## 동시 phase와 장애

기본 생성기는7개 상위 action이다. 각 wave의 준비 주문2개 뒤 concurrent action에서 아래 과정을 수행하고 마지막에 추가 정상 재시작을 한다. seed는 수량·후속 매도 계정/가격을 선택하며 OS 도착 순서를 정하지 않는다.

1. 초기 phase의4요청을 각 중계가 모두 받은 뒤 공통 gate를 연다. 같은 매수 ID/내용을 두 클라이언트가 동시에 제출하고 한 응답은 헤더 전 유실, 다른 응답은 본문 절단으로 끊는다. 다른 클라이언트는 준비 주문 취소를, 마지막은 고유 요청을 전달 보류한다.
2. 선택 요청의 upstream 완료를 확인한 뒤 downstream 응답을 계속 보류한다. 그 HTTP 호출이 아직 반환하지 않은 상태에서 snapshot을 게시한다. snapshot은 owner의 명령 적용 경계이며 전체 큐를 비우는 barrier가 아니다.
3. 초기 클라이언트가 합류한 뒤 보류 요청의 결과 GET에서404 UNKNOWN을 기록한다. 서버 전달을 아직 하지 않은 실험상의 gate를 알고 있더라도, 일반 API의404를 미접수 보장으로 바꾸지 않는다.
4. 재시도 phase의4요청도 공통 gate로 함께 시작한다. 동일 주문·취소 ID 재시도, 보류 요청의 같은 ID/내용 재시도와 새 후속 매도를 처리한다. 이후 늦은 원래 요청도 서버에 전달해 같은 최초 결과를 받는지 확인한다.
5. 모든 클라이언트/중계 핸들러를 합류한 뒤 정상 종료하고 실제 WAL을 읽는다. 검증 후 snapshot+tail 및 별도 Genesis+WAL 사본으로 정상 복구하고 조회·재시도를 반복한다.

두 phase는 모두4개 고정 client ID0~3을 사용한다. 초기 phase에는 held-original이 정확히 하나이며 다른 초기 요청이 같은 키를 미리 제출할 수 없다. 재시도 phase에는 응답 유실을 넣지 않고 held 요청을 같은 내용으로 포함한다. replay 명세도 입력/키/클라이언트/장애·snapshot 경계를 검증한다. 상위 action과 phase 시도 budget은256, snapshot/restart control은32로 제한한다. 기본 전체 context2분, wave30초 제한을 사용한다. upstream HTTP client는5초, 응답 보류를 포함한 downstream 호출은 wave context30초로 제한한다. gate/모델·중계는 개발 도구에만 있고 거래 서버 저장/owner에 새 hook을 넣지 않는다.

## 실제 저장 순서로 비교

클라이언트 완료 순서나 제출 배열 순서로 모델을 Apply하지 않는다.

- 실행 중 upstream 최초 결과의 순번으로 임시 모델을 만들어 HTTP 잔고·현재 주문·호가·체결·최초 결과 GET/POST를 대조한다. 이 임시 순서는 아래 실제 WAL과 전체 입력/순번이 일치해야만 채택한다.
- runtime을 종료해 소유 잠금을 해제한 뒤 WAL을 연다. 이전 prefix 불변, 모든 계획 키가 정확히 한 번 기록됐는지, 입력 전체/순번/누락·중복을 검사한다. 결과 응답뿐 아니라 이미 저장됐어야 할 모든 계획 명령이 대상이다.
- WAL 순서대로 순차 엔진을 재생해 각 upstream 최초 응답과 완전한 client 응답 fingerprint를 비교한다. 유실한 client의 Outcome은 UNKNOWN으로 남기며 중계가 받은 성공을 client 성공으로 재분류하지 않는다.
- 동시 snapshot의 전체 checkpoint를 해당 WAL prefix의 모델과 비교한다. 복구 후에는 모든 잔고·공급량·주문·호가·체결·최초 결과 HTTP, FIFO를 포함한 전체 checkpoint를 확인한다. live HTTP의 공개 호가 합계와 snapshot/checkpoint의 FIFO 검증을 구분한다.
- snapshot 이후 tail이 있어야 하며 재시작 report의 선택 snapshot·tail 건수를 확인한다. 별도 WAL-only 사본은 전체 재생도 검사한다. 검증 조회·재시작·같은 ID POST는 추가 WAL 기록을 만들면 안 된다.

순차 엔진은 서버와 같은 매칭 구현이다. 저장·전송·복구 경로를 대조하며 독립 매칭 oracle을 새로 만들었다는 뜻은 아니다. 기존 독립 매칭 모델과 fuzz/race 검사는 유지한다. 서로 경쟁하는 주문·취소의 결과는 유효한 저장 순서에 따라 달라질 수 있다.

## 기록된 순서 재현과 실패

`scenario.json`은 실행 전 입력·phase/gate 계획이다. 완전한 저장 순서가 확인되면 `replay-<action>.json`을 별도로 저장한다. 해당 concurrent payload의 recorded에는 새 명령의 순서와 `snapshotAfter`(wave 시작 이후 명령 수)가 있다. 원래 명세나 이전 기록을 덮어쓰지 않는다.

기록 replay는 다음 **고유 키**를 앞 키의 upstream 완료 뒤에만 보내고, 같은 키 복사본은 동시에 보낼 수 있다. 지정한 prefix에서 다음 고유 키를 보류하고 snapshot을 만든 뒤 gate를 연다. 따라서 동일 입력의 WAL 순서·snapshot 경계·최종 논리 상태를 재현하면서 같은 ID의 병행 호출과 응답 유실을 유지한다. runId·포트·소요 시간·goroutine 스케줄까지 같다는 뜻은 아니다.

실패에는 failed.json과 표준 출력에 한 명령 재현 안내가 남는다. 논리 응답/체크포인트 검증 전에 유효한 저장 순서를 확보했으면 해당 replay 명세를 우선한다. 완전한 순서를 얻기 전의 취소·환경/저장 장애는 마지막 유효 replay 또는 scenario.json으로 입력·gate를 재실행하며 미관측 interleaving이나 디스크 오류의 동일 발생을 보장하지 않는다. 부분 작성된 replay 파일은 재현 대상으로 선택하지 않는다. 디스크 기록 자체가 실패하면 기존 중간 파일/표준 오류도 함께 확인한다.

| 파일 | 내용 |
| --- | --- |
| `scenario.json`, `config.json`, `build.json` | v2 입력·seed/초기 잔고·실행 코드/바이너리 정보 |
| `events.jsonl`, `http.jsonl` | phase·snapshot의 응답 대기 증거·복구 경계, 주 스레드의 조회/검증 HTTP |
| `concurrent-*-observations.json` | phase/client별 명령·client Outcome/지연과 중계의 upstream 상태/본문. 두 관측을 구분 |
| `concurrent-*-wal.json`, `concurrent-*-live-order.json` | 실제 WAL 순번/입력과 그 순서에 대조된 live 검사 순서 |
| `concurrent-*-snapshot-{expected,actual}.json` | 동시 snapshot과 실제 저장 prefix의 전체 상태 |
| `replay-*.json` | 관측한 실제 저장 순서와 snapshot 경계를 포함한 별도 재현 명세 |
| `restart-*`, `wal-only-*`, `final-*`, `run/` | snapshot+tail·전체 WAL 재생/최종 상태와 이번 실행이 소유한 저장 파일 |
| `summary.json`, `manifest.json`, `completed.json`, `failed.json` | 완료/실패와 검사 건수·재현 명령, 모든 종료/출력 뒤 성공 봉인 |

기존 전송 오류 분석 보고서의 recoverycheck 입력은 v1 transport-loss 자료용이다. 새 v2 자료는 위의 summary/관측/WAL/상태 파일을 사용하며 기존 v1 분석 입력으로 섞지 않는다.

## 자동 검증

```sh
scripts/go test -race ./tools/recoverycheck -count=1 -timeout=180s
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

portable은 명세·기록 gate·실제 중계 취소/합류·저장 미지원 경계를, native는 seed17/42/701과 각 recorded replay·실패/원본 보존·초기 요청이 snapshot 뒤에 남는 강제 경계를 검사한다. CI는 기존 random-recovery-* 경로에 실제 성공/실패 fixture를 보존한다. 정상 HTTP/runtime 종료/재기동이며 SIGKILL·실제 전원 장애 시험으로 해석하지 않는다. [실제 실행 근거](benchmarks/concurrent-recovery.md)를 함께 읽는다.
