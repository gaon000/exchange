# 동시 요청 SIGKILL·복구 검증

상태: Completed
시작일: 2026-11-17
종료일: 2026-11-17

## Purpose / Acceptance

테스트용 자식 서버에 4개 동시 주문/취소/같은 ID 요청을 보내 WAL 실제 Append 동기화 후 적용/응답 전, snapshot Publish 호출 전·성공 반환 후 경계에서 SIGKILL한다. 실제 WAL 순서로 복구 상태·잔고·FIFO·체결·최초 응답과 재시도 중복 방지를 대조한다. 모든 미응답 시도는 UNKNOWN으로 보존하며 후속 GET/재시도 관측을 별도로 기록한다. 새 폴더의 한 명령 실행/실패 재현과 입력·경계·저장 순서·봉인 증거를 제공한다.

## Progress

- [x] 기존 동시 요청 도구·test-only HTTPTestHooks와 실제 프로세스 장애 회귀 확인, 기능 브랜치 생성.
- [x] 제한된 명세/CLI·test binary 실행기·소유 자식 수명/신호 확인.
- [x] 세 경계 실제 동시 요청·WAL 모델·복구/재시도/최초 응답 검사.
- [x] 정상/실패/입력 보호·기록 순서 replay 회귀·실제 한 명령 실행.
- [x] 전체 portable/native CI·명령/근거·한계 문서.

## Surprises & Discoveries

runtime의 기존 private 장애 경계는 http_bridge_test.go에서만 외부 test package에 노출한다. 일반 빌드에 장애 제어 옵션을 추가하지 않고 `go test -c`의 전용 실행기를 사용한다. 현재 --concurrent는 정상 종료/restart이므로 새 검사와 증거를 구분한다.

초기 컴파일에서 diagnostic Outcome의 Result 필드를 Response로 잘못 참조한 부분을 수정했다. macOS의 t.TempDir은 /var symlink 경로일 수 있으므로 테스트 소유 parent만 EvalSymlinks로 실제 경로를 구하고, 사용자 출력의 symlink 거절 계약은 유지했다. stdout은 직접 소유하는 pipe로 수집해 cmd.Wait가 읽기 중 pipe를 닫는 경합을 피하고 종료/reap·수집 합류 후에만 로그를 봉인한다. 초기 실패 로그를 보존하고 최종 관련/전체 회귀를 통과했다.

## Decision Log

- 2026-11-17: `scripts/concurrent-crash.sh`가 runtime test binary를 빌드하고 전용 command test만 선택한다. 기존 서버/WAL/snapshot 파일·저장 계약은 변경하지 않는다. 추가 dependency 없음.
- 2026-11-17: 자유 실행의 WAL 순서를 oracle로 삼고 관측한 고유 키 순서를 별도 replay 명세에 기록한다. 기록 확보 전 환경/취소 실패는 입력/경계 재실행이며 동일 OS interleaving을 보장하지 않는다.
- 2026-11-17: 전원 장애는 계속 미완료·보류이고 v1 전체 계획은 Blocked다. 장시간 성능 측정·commit/merge/push는 이번 구현 요청에 포함하지 않는다.

## Outcomes & Retrospective

구현/검증을 완료했다. 최종 원본 `var/concurrent-crash-final-20261117`과 기록 replay `var/concurrent-crash-final-replay-20261117`의 사용자 명령이 PASSED다. 같은 test binary hash에서 각84개 봉인 파일의 크기/hash·manifest/completed를 확인했다. 세 경계의 crash/final WAL과 전체 최종 checkpoint는 runId/tradeId run prefix 정규화 뒤 일치했고 최종 replay-005.json은 bytes 동일했다.

선택 snapshot+tail은 WAL 동기화 뒤2+1, snapshot 게시 전2+4, 게시 후5+1이었다. 재시도 후 고유 명령 수는5/6/6, 관측 체결은2/3/3이다. 모든 원래 미응답12건은 UNKNOWN 유지, 후속 GET의404 두 건도 미확정으로 보존했다. 초기/후속 성공 응답8개가 실제 WAL/최초 결과에 남고, 같은 ID 동시 재시도12회 및 두 번째 재기동 뒤 고유 ID17개 재시도에서 중복 저장/상태 변경이 없었다.

seed17/42의 원본+기록 replay·seed701의 출력 실패/failed.json/새 폴더 재현, 네 요청 진행 중 취소/reap·잠금 해제, 잘못된 저장 순서/키·명세/기존 출력 보호를 회귀로 검사했다. 실제 구현·명령은 [안내](../../concurrent-crash-recovery.md), 결과는 [보고서](../../benchmarks/concurrent-crash-recovery.md)와 [JSON](../../benchmarks/20261117-concurrent-crash-recovery.json)에 정리했다. 기존 순차/동시 정상 재시작·프로세스/HTTP 시험은 전체 CI에 유지했다.

전원 장애는 미완료·보류, 원래 v1 전체 계획은 Blocked 유지다. 동일 매칭 엔진의 WAL 모델이며 독립 매칭 oracle·배포 바이너리·장시간 성능을 새로 주장하지 않는다. 최초 구현 완료 시점에는 commit/merge/push나 새 GitHub CI를 수행하지 않았다.

## 발행 검토

2026-11-17 후속 사용자 요청으로 검토·커밋·master 병합/푸시와 해당 병합 SHA의 GitHub CI 확인을 진행한다. 소유 자식 종료/reap·클라이언트 합류, 실제 Append/Publish 경계·WAL prefix/전체 상태·최초 결과와 UNKNOWN 분리, 새 출력/기록 replay 경로를 검토했다. 소스 변경 없이 원본/재현 각84파일의 봉인 bytes를 다시 확인했고 발행을 막는 결함은 발견하지 못했다. 기존 두 전체 로컬 CI는 동일 소스의 검증 근거로 유지한다.

## Validation and Acceptance

관련 runtime race 회귀를 통과했다. 전체 `bash scripts/ci.sh portable`: var/ci/portable.PpotZe, Go255개/Python111개·format/vet/build·진단 smoke exit0. 전체 `bash scripts/ci.sh native`: var/ci/native.bQ2WAu, Go race301개(명시적 child helper skip1)/Python111개·실제 APFS/기존 HTTP·전송 보고서/진단 smoke exit0. 새 폴더의 세 경계 실행/기록 replay와 실패 명세 재현, 모든 봉인 bytes를 확인했다. child의 실제 SIGKILL WaitStatus와 종료 전 네 호출 진행 중·snapshot 대기 후속 성공을 기록했다. 기존 입력/출력은 보존했다. 실제 전원 장애 증거로 해석하지 않는다.
