# 전송 오류·UNKNOWN 진단과 응답 유실 재현

상태: Completed
시작일: 2026-11-16
종료일: 2026-11-16

## Purpose / Acceptance

최근10분 켬의 전송 오류/UNKNOWN1건은 일반 Detail만 있어 정확한 실패 단계를 알 수 없었다. bench HTTPExecutor의 선택적 failureStage/errorType에 실패 위치와 제한된 오류 분류를 기록한다. 과거 표본은 필드 없이 읽으며 서버 API·WAL/snapshot·certainty/측정 판정은 유지한다. 관측이 부족한 오류는 round_trip/other로 남기고 접수 여부를 추정하지 않는다. 새 dependency는 없다.

기존 recoverycheck의 고정 response-loss 시나리오/저장 명세 replay를 추가한다. 실제 loopback HTTP 중계에서 헤더 전 유실·본문 절단·원래 요청 전달 보류를 주입한다. 보류 중404 UNKNOWN을 조회한 뒤 동일 ID 재시도와 늦은 원래 요청이 하나의 결과/순번만 만드는지 확인한다. 실제 체결·잔량 취소/예약금 반환·open 주문·후속 체결로 첫 응답과 현재 상태를 구분한다. WAL-only 및 snapshot+tail 재시작·최초 응답/전체 checkpoint/잔고·공급량·호가·체결·실제 WAL을 비교한다.

## Progress

- [x] 현재 코드·계약·기존 검증/명세 확인, 기능 브랜치 생성.
- [x] 제한된 단계/유형·동시 trace 수명·표본 직렬화/과거 호환·단위/경계 검증.
- [x] 고정 응답 유실 명세/재현 명령·실제 HTTP/APFS 재시작·중복 처리 방지 검증.
- [x] 전체 portable/native CI·실제 한 명령 실행·문서 갱신/완료 기록.

## Surprises & Discoveries

기존 wireResponse는 transport/read/close 오류를 Join하지만 Detail은 원인을 버린다. trace callback은 실패한 RoundTrip 반환 뒤에도 호출될 수 있어 기존 atomic 관측 수명을 유지한다. 404 UNKNOWN은 단순 저장 결과 부재이며 늦은 원래 요청의 접수를 배제하지 않는다.

전체 CI 첫 실행은 Go portable244/native284(race) 통과 뒤 Python overlay hash gate에서 실패했다. runner.go Outcome의 선택 필드를 trace template에도 동일하게 반영하고 기준 hash를 갱신했다. 첫 실패 자료 `var/ci/portable.bjpukF`, `var/ci/native.d9uscH`를 보존한다. 초기 권한 제한의 loopback bind 실패는 권한을 갖춘 실행에서 재확인했다.

## Decision Log

- 2026-11-16: 두 선택적 enum 필드만 추가한다. 원문 URL/본문/임의 오류 문자열을 기록하지 않고 오류 분류를 whitelist로 검증한다. 기존 UNKNOWN과 timeout 분류는 보존한다.
- 2026-11-16: 서버의 장애 hook은 추가하지 않고 별도 loopback 중계에서 응답을 끊는다. 채널 gate로 순서를 고정하고 원래 요청/재시도가 중복되더라도 모델·실제 WAL 순번이 한번만 증가해야 한다.
- 2026-11-16: 새 부하 측정·커밋/merge/push는 범위 밖이다. 실제 전원 장애는 미완료·보류, v1 Blocked 유지.

## Outcomes & Retrospective

고정 실측 `var/transport-unknown-20261116`과 명세 replay `var/transport-unknown-replay-20261116`가 통과했다. 전체 파일 hash는 각 manifest/completed.json에 있다. 8 actions/3 전송 장애/404 UNKNOWN1회, 고유 명령5·체결2·정상 재시작3·WAL-only 비교3회다. 재시작 선택은 WAL2, snapshot2+tail1, snapshot3+tail2이며 잔고·FIFO·전체 checkpoint·최초 응답 bytes/실제 WAL이 일치했다. bob 잔여 매수 예약금180atoms, alice 취소 후 base 예약0을 독립 정수 기대값으로 확인했다. 최초 OPEN 응답과 후속 부분 체결의 현재 주문을 구분했다.

기존6실행 재검증 명령:

```sh
python3 tools/diagnostics/experiment.py compare --spec docs/benchmarks/20261116-latest-soak-spec.json --output var/transport-legacy-comparison-20261116
```

`report.json`의 sourceSHA256을 제외한 모든 최상위 값이 기존 `docs/benchmarks/20261116-latest-soak-comparison.json`과 일치했다. source hash는 이번 소스 변경 때문에 다르며 기존 필드 없는 표본·UNKNOWN·수치/판정은 유지됐다. 과거 원본과 보고서를 수정하지 않았다.

최종 `bash scripts/ci.sh portable` (`var/ci/portable.X25Tru`)와 `bash scripts/ci.sh native` (`var/ci/native.GQ1GQ7`)가 exit0으로 통과했다. portable244/native race284개(명시적 자식 helper skip1), 양쪽 Python103개·format/vet/build·portable/native 진단 smoke를 확인했다. native smoke는 실제 trace·OS I/O·거래/복구와 자료 package/복원/재생성·의미 변조 거절을 포함한다. `git diff --check`, 새 파일 공백·문서 상대 경로 확인도 통과했다. 11/16 구현 완료 당시에는 push하지 않아 실제 GitHub CI의 새 실행을 확인하지 않았다. 과거 전송 오류1건의 정확한 원인을 이번 코드로 소급 확정하지 않는다. 성능 측정/계측 비용 비교·SIGKILL/실제 전원 장애를 새로 수행하지 않았으며, 11/16 구현 완료 당시에는 커밋/merge/push도 수행하지 않았다.

## 2026-11-17 검토·발행

사용자가 검토·커밋·master 병합/푸시·실제 GitHub CI 확인을 승인했다. 11/16 구현 완료 당시의 미발행 기록과 구분한다. 원격 master와 로컬 기준은 `7b731d2dd94209148dfec192cecf4dc2379cda59`로 일치했다.

검토에서 응답 중계의 종료 합류를 보완했다. `httptest.Server`는 Hijack 시 연결을 추적 wait group에서 제거하므로 Close만으로 보류된 원래 요청의 핸들러 반환을 보장하지 않는다. gate 해제/context 취소 뒤 서버 연결을 닫고 별도 핸들러 wait group까지 합류한다. 실제 loopback 연결을 끊은 핸들러를 보류해 종료가 먼저 반환하지 않는 경계 회귀를 추가했다. UNKNOWN/최초 결과/저장 계약과 운영 dependency는 바꾸지 않는다.

`scripts/go test -race ./internal/bench ./tools/recoverycheck -count=1 -timeout=120s` 통과. 후속 전체 로컬 CI도 통과했다. `bash scripts/ci.sh portable` (`var/ci/portable.NV6j0i`)는245개, `bash scripts/ci.sh native` (`var/ci/native.5iYX5g`)는race285개(명시적 자식 helper skip1)를 검증했고 각 Python103개·format/vet/build·진단 smoke가 exit0으로 끝났다. 기존 두 증거 묶음 각44개 파일의 hash, 과거 비교 보고서 수치/판정, overlay 타입/해시·문서 경로/공백도 다시 확인했다. 검토 뒤 남은 알려진 결함은 없다. GitHub CI는 푸시 후 원격 병합 SHA에 연결된 모든 job을 확인해 최종 답변으로 보고한다. 실제 전원 장애는 계속 미완료·보류이며 v1 Blocked를 유지한다.
