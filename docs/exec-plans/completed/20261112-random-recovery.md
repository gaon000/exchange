# 고정 seed 거래·복구 통합 검증

상태: Completed
종료일: 2026-11-12

## Purpose / Big Picture

주문·취소·동일 ID 재시도/충돌·snapshot·정상 재시작을 고정 seed의 짧은 명령열로 조합한다. 실제 loopback HTTP/APFS runtime을 순차 메모리 엔진과 비교하고, 저장한 WAL의 순서·snapshot 복구·별도 사본의 Genesis+WAL 복구 및 최초 HTTP 응답의 불변성을 검증한다. 실패 시 명세와 단계/요청 기록을 남기고 한 명령으로 새 디렉터리에서 재현한다. 기존 엔진 독립 매칭 모델을 대체하지 않으며 실제 전원 장애는 계속 미완료·보류다.

## Progress

- [x] 현재 테스트/HTTP/저장 계약 확인, feature branch 생성.
- [x] bounded 시나리오 생성·명세 검증·재현 CLI/스크립트 구현.
- [x] 실제 HTTP/파일·snapshot/재시작·최초 응답·WAL 사본 대조와 실패 보존 구현.
- [x] 회귀·고정 seed 실제 실행·전체 CI·문서 검증.

## Decision Log

- 2026-11-13: 후속 요청으로 변경 검토·커밋·master 병합/푸시 및 해당 커밋의 실제 GitHub CI 확인을 진행한다. 검토에서 upload-artifact의 기본 숨김 파일 제외가 `.lock`을 누락시켜 다운로드 후 봉인 검증을 깨뜨릴 수 있음을 확인했다. 기존에 선택한 CI 자료 경로에만 `include-hidden-files: true`를 적용하며, 실제 원격 artifact 다운로드/봉인 검증으로 확인한다.
- 2026-11-12: production 의존성이나 서버 테스트 훅을 추가하지 않는 독립 `tools/recoverycheck` 개발 도구를 사용한다. snapshot은 공개 runtime API로 명시적으로 요청하고 거래/조회는 실제 HTTP로 전송한다.
- 2026-11-12: 최초 버전은 순차 명령과 정상 drain/reopen이다. 이전의 동시 HTTP·SIGKILL/12개 장애 경계 테스트는 그대로 유지하며 이번 실행을 강제 종료·전원 장애 증거로 표현하지 않는다.
- 2026-11-12: seed와 별개로 완전한 versioned action list를 보존해 생성기 변경 후에도 명령열을 재입력한다. runId/port는 새 실행에서 발급한다. 원본 실패 자료를 덮어쓰거나 재실행에 사용하지 않는다.

## Surprises & Discoveries

기존 엔진 테스트는 독립 모델과 checkpoint 복원을 포함하고, HTTP 테스트는 고정 concurrent seed와 한 번의 복구를 검증한다. 새로운 범위는 여러 중간 snapshot·WAL tail·재시작을 섞은 재현 가능한 명령열과 실패 자료 보존이다. 순차 메모리 비교 엔진은 같은 매칭 구현이므로 persistence/transport 비교의 oracle이며 독립 매칭 구현이라고 주장하지 않는다.

첫 실행의 WAL-only 사본은 `.lock`이 없어 step 12에서 기동을 거절했다. 원본 실패 폴더 `var/random-recovery-initial-17`을 유지하고 사본에 빈 잠금 파일도 복사하도록 보강했다. 저장된 `scenario.json`은 처음의 명세 해시 그대로이며 수정된 검사기로 replay를 통과했다. snapshot 전체 상태 읽기에는 검증 snapshot의 게시가 필요하므로, 선택 snapshot/tail 검사를 먼저 하고 그 뒤 게시한 검증 snapshot을 다음 restart의 기준으로 추적한다. 순번 0의 snapshot과 snapshot 부재도 별도로 구분한다.

## Validation and Acceptance

생성 명세의 결정성/한도/ID 충돌·재시도·필수 거래 경로, 미지원 플랫폼 무변경, 실제 세 seed와 명세 replay, 의미 불일치 거절·실패 자료/출력 재사용 보호를 검사한다. 매 restart 직전 WAL을 검사하고 복구 report의 snapshot/tail 수, HTTP 현재 상태와 최초 결과/재전송 bytes, complete checkpoint, 별도 Genesis+WAL 사본과 비교한다. CI는 작은 seed 집합만 실행하며 새 성능 캠페인이나 실제 장비 제어는 하지 않는다.

## Outcomes & Retrospective

`tools/recoverycheck`와 `scripts/random-recovery.sh`, 사용 안내·CI 필수 대표 검사/fixture artifact 보존을 구현했다. CLI는 생성한 전체 action 명세와 현재 빌드/설정, HTTP 요청·응답·오류, 단계/복구 report, 기대/관측 전체 checkpoint, 실제 run과 WAL-only 사본을 보존한다. 정상 종료·trace close/sync 뒤 파일별 해시 manifest와 완료를 게시하고, 실패 시 단계/오류와 한 명령 replay를 남긴다. 미지원 저장 환경은 출력 생성 전 거절한다.

최종 CLI에서 seed 17·42·701의 각 64 actions를 통과했다. 세 실행 합계 98개 고유 저장 명령, 정상 재시작 24회와 별도 Genesis+WAL 복구 24회, snapshot 뒤 WAL tail이 있는 재시작 17회를 확인했다. 각 단계에서 메모리 모델 불변식과 실제 순번을 확인하고 각 snapshot/restart에서 HTTP 현재 상태/최초 응답 및 complete checkpoint를 비교했다. 실패 명세 32 actions도 원본을 유지한 새 폴더에서 통과했고, 네 성공 묶음의 SHA-256 봉인을 기존 Python verifier로 확인했다.

새 장시간 부하·최적화·SIGKILL 주입·실제 OS/전원 제어는 하지 않았다. 이 도구의 메모리 oracle은 같은 엔진이며 독립 매칭 모델은 기존 엔진 테스트가 담당한다. 전체 v1은 실제 전원 장애 필수 증거가 없어 계속 Blocked다. 커밋·병합·푸시·GitHub 원격 CI는 이번 구현 요청에 포함하지 않는다.


실제 검증 기록:

- 새 portable 회귀 5개: 결정적 생성/경계, 필수 prefix의 독립 정수 기대값/불변 최초 결과, strict JSON/옵션/명세 거절, 원본/출력/링크/크기 보호, 저장 action list 재구성. 별도 미지원 저장 검사 1개는 실제 파일을 만들지 않는 것을 확인한다.
- 새 native 회귀 4개: 세 seed 전체 경로, saved-plan replay/취소 실패 보존/원본 보존/출력 재사용 거절, 순번 0 snapshot 및 모순된 HTTP 상태 거절, stdout 기록 오류 때 완료 게시 차단. 최초 native 단독 race 검사도 `var/random-recovery-native-tests.log`에 보존했다. 마지막 출력 오류/바이너리 해시 보강은 아래 전체 CI에서 검증했다.
- `bash scripts/ci.sh portable`: 221 tests / 22 packages, Python 53개·진단 package/restore smoke·포맷/vet/build 통과. `var/random-recovery-ci-portable.log`, `var/ci/portable.VCDcft`.
- `bash scripts/ci.sh native`: race 258 tests / 22 packages, 명시적 child-helper skip 1개, Python 53개·native APFS/HTTP/trace/I/O/package restore smoke 통과. `var/random-recovery-ci-native.log`, `var/ci/native.0CHqyN`.
- native CI 소유 자료는 `var/ci/native.0CHqyN/random-recovery-*/`에 남는다. 예상 취소·출력 실패 fixture와 실제 검사 실패를 구분한다. workflow artifact 경로에도 추가했으나 원격에서의 실행/수집은 아직 확인하지 않았다.
- 실제 최종 CLI: `var/random-recovery-20261112-final17`은 64 actions/저장 31/재시작 11, `final42`는 64/35/6, `final701`은 64/32/7이다. 세 seed의 완료/명령별 최초 응답·state 및 WAL 검증을 통과했다.
- `var/random-recovery-20261112-final-replay`는 이전 실패의 32 actions를 저장 16/재시작 5회로 통과했다. 초기 명세 SHA-256 `7caac221e973f373b2a50c54d1cebcf38c9d37950adc4bab193a76a66e5ee234`가 동일하다. 기존 실패는 삭제·수정하지 않았다.
- `var/random-recovery-20261112-proof.json`: 네 성공 묶음의 기존 verifier 통과(각 134/81/102/65개 파일), 원래 실패 명세 일치·원본 실패 파일 존재를 기록했다. 출력은 로컬 Git 제외 자료다.
- [사용 안내](../../random-recovery.md)와 문서 지도/개발 기준/구조, 실행 계획 목록을 연결하고 문서 링크·새/추적 파일 공백·실행 스크립트 구문을 확인한다.

2026-11-13 발행 전 검토: `scripts/go test -race ./tools/recoverycheck ./tools/civerify -count=1 -timeout=120s` 재통과(`var/random-recovery-review-20261113.log`). 주문/취소·최초 응답·snapshot/tail/Genesis 재생·입력/출력/실패 보존과 CI 필수 검사 연결을 검토했다. 거래 서버 production 코드는 변경하지 않았으며, 원격 수집의 숨김 잠금 파일 누락만 workflow에서 보강했다. 커밋/병합/푸시는 Git 기록, 실제 원격 CI와 artifact 검증은 해당 마스터 커밋의 실행을 확인한다.
