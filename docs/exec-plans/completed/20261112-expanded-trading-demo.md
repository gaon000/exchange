# 거래 규칙과 snapshot·프로세스 복구 데모

상태: Completed
종료일: 2026-11-12

## Purpose / Big Picture

사용자 요청에 따라 한 명령 거래 데모에 자기 거래 차단·예약금 반환·중복 ID 충돌과 snapshot/프로세스 강제 종료 복구를 추가한다. 기본 all과 normal/snapshot/crash 선택 실행을 제공한다. 실제 전원 장애는 계속 보류한다.

## Progress

- [x] 기존 HTTP/프로세스/기대값과 CI 구조 확인·feature branch 생성.
- [x] 공개 HTTP·고정 정수 기대값의 거래 규칙 시나리오.
- [x] 정상 WAL 재시작·snapshot 선택/재생 수·소유 자식 SIGKILL 복구와 원본 보존.
- [x] 재시도 전 조회·최초 응답 bytes·잔고/주문/호가/체결·예약금 검증.
- [x] 대상/전체 Go race·실제 all/snapshot/crash·중단/오류 정리·한 명령 실행과 문서 갱신.
- [x] 사용자 재개 요청 뒤 portable/native 전체 CI 스크립트 완료.

## Surprises & Discoveries

작업 시작 시점의 데모는 3개 고유 명령·정상 재시작만 시연했다. 기존 native 내부 검증에는 장애 복구가 있었지만 공개 CLI 데모에는 없었다. snapshot은 공개 serve의 주기 옵션/이벤트로 기다리며 완료된 snapshotSeq를 확인할 수 있다.

## Decision Log

- 2026-11-12: 여유 약 10GiB에서 portable/native 전체 CI를 재개해 두 모드 모두 통과했다. 이전 공간 부족 실패를 보존하고 구현 계획을 완료한다. GitHub 발행과 원격 CI는 실제 실행 결과를 별도로 확인한다.
- 2026-11-12: 사용자의 명시적인 검토/발행·CI 확인 요청으로 검증을 재개한다. 실제 전원 장애 보류는 유지하며 새 성능 측정은 실행하지 않는다.
- 2026-11-12: 사용자가 추가 공간 확보가 어려워 CI 미완료 기록을 요청했다. 구현·이미 통과한 테스트/실제 데모 증거는 유지하고 전체 CI 성공으로 표시하지 않는다. 추가 CI/성능 측정은 자동 실행하지 않는다.

- 2026-11-12: 거래/저장/HTTP 계약은 변경하지 않고 demo CLI만 확장한다. production dependency는 추가하지 않는다.
- 2026-11-12: 기본 all은 기존 3개 거래에 maker ask·self buy·가격 개선 buy·복구 후 tail 주문/취소를 더한다. MaxCommands=8이며 같은 키 충돌/재시도는 순번을 늘리지 않아야 한다. 기대값은 독립적인 정수 상수다.
- 2026-11-12: all은 WAL 정상 복구→seq6 snapshot 완료→snapshot 복구→seq7 승인→소유 서버 SIGKILL→snapshot6+WAL1 복구→seq8 취소를 확인한다. snapshot 모드는 정상 재시작으로 snapshot+tail을, crash 모드는 snapshot 없이 WAL 강제 종료 복구를 확인한다. 기존 normal은 3명령 데모를 유지한다.
- 2026-11-12: 매 재기동에서 상태와 최초 결과를 GET으로 검증한 뒤에만 POST를 허용한다. 강제 종료는 직접 만든 자식만 Kill/Wait하고 실제 SIGKILL 종료와 로그 I/O 오류를 구분한다. 기존 출력 재사용·외부 서버/OS 종료는 하지 않는다.

## Outcomes & Retrospective

구현과 대상 race·전체 Go race 및 실제 한 명령 실행을 통과했다. all은 snapshot6/WAL0 복구와 snapshot6/WAL1 강제 종료 복구를 확인했고 8명령 최초 응답·예약금·충돌을 검증했다. normal/snapshot/crash와 중단/출력 오류도 native 테스트를 통과했다. 최초 portable 전체 CI는 마지막 진단 smoke의 디스크 조건 때문에 실패해 보류했지만, 사용자의 검토·발행 요청 뒤 공간 조건을 확인해 전체 CI를 재개했다. portable 210개·native race 243개(명시된 child-helper skip 1개)와 각 Python 32개·진단 smoke가 모두 통과했다. 검토에서 추가 수정이 필요한 문제는 발견하지 않았다. 실제 전원 장애는 이 계획의 완료 범위 밖이며 계속 보류다.

## 검증

일반 HTTP 오류/409 처리, 소유 child kill 합류, native all/snapshot/crash 실제 파일/프로세스, 실패/중단 정리와 기존 normal 회귀를 검증한다. CI의 필수 실행 확인에 새 native 검증을 연결한다. 작업 시작 시 캐시/일반 bin이 정리돼 최초 빌드에서 재생성했다. 새 성능 측정과 전원 장애 검증은 하지 않는다.

- 실제 증거: var/expanded-demo-targeted.log(대상 race), var/expanded-demo-all-race.log(전체 race), var/expanded-demo-ci-portable.log(Go 210·Python 32 통과, smoke 공간 미달), var/expanded-demo-one-command.log와 var/demos/demo-468493379(summary PASSED, snapshot 복구 6+0/강제 종료 복구 6+1, powerLossTested=false). 기존 측정 원본과 고정 빌드는 삭제하지 않았다.
- 최종 한 명령 증거: var/expanded-demo-one-command-final.log와 var/demos/demo-439742113(summary PASSED, WAL6·snapshot6/WAL0·snapshot6/WAL1 복구, 매번 POST 전 GET 검증).
- 검토 시 샌드박스 내부 대상 테스트는 loopback bind 권한으로 실패했다(var/expanded-demo-review-targeted.log). 허용된 실행 환경에서 전체 CI로 재검증했다.
- 최종 로컬 CI: var/expanded-demo-review-ci-portable.log와 var/ci/portable.zllW7J(210개·20 package), var/expanded-demo-review-ci-native.log와 var/ci/native.dfAVzW(race 243개·20 package, 명시된 child-helper skip 1개). 두 모드 모두 Python 32개 및 fresh run·무결성·분석·출력 재사용 거절·의미 변조 거절 smoke를 통과했다. 포맷·vet·빌드·공백 검사도 포함한다.
