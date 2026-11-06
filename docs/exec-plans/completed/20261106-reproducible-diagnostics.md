# 실험 도구의 저장소 편입과 재현 명령

상태: Completed
종료일: 2026-11-06

[운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

최근 네 진단 단계의 var 전용 실행·전체 복구·원본 재집계·타이머·trace/OS I/O 도구를 저장소에 포함한다. 새 checkout에서 고정된 Go/Python 표준 도구만으로 짧은 smoke와 기존 10분 조건을 실행하고, 보존된 묶음을 경로·시각 하드코딩 없이 검증·분석할 수 있게 한다. 기존 대용량 원본과 당시 보고서는 변경하지 않는다.

## Progress

- [x] 현재 master 4484c71·clean tree와 관련 도구 조사, feature branch 생성.
- [x] 재사용 Go probe/재집계 CLI와 Python run/analyze/verify 정리.
- [x] 원본 보존·새 경로·실패·sampler·archive 검증과 테스트.
- [x] 짧은 실제 실행·기존 원본 재분석·전체 검사·명령 문서화.

## Surprises & Discoveries

기존 도구에는 날짜별 parent·실행 이름·600초·event 시각과 assert가 하드코딩돼 있다. 일부 source overlay는 프로덕션 파일의 전체 사본이라 원본 hash가 다르면 실패하도록 고정한다. trace/OS I/O 실행은 macOS/APFS/cgo가 필요하며 커널 내부 추적은 제공하지 않는다.

## Decision Log

- 2026-11-06: tools/diagnostics의 표준 Python CLI와 tools/timerprobe·tools/experimentcheck Go CLI로 정리한다. 외부 production dependency 추가 없음.
- 2026-11-06: 기본 실행은 smoke다. soak/trace/io/timer를 선택하고 긴 실행은 ten-minute preset을 명시한다. 실제 장시간 재측정은 이번 편입 검증으로 반복하지 않는다.
- 2026-11-06: 삭제는 새 실행 중 검증한 archive 원본에만 명시 옵션으로 허용한다. 기본 원본 보존·sealed hash 검증·출력 재사용 거절을 유지한다. 기존 측정 실패와 성능 미채택을 바꾸지 않는다.

## Outcomes & Retrospective

tools/diagnostics의 run/analyze/verify, tools/timerprobe, tools/experimentcheck와 source-bound 계측 overlay를 구현했다. 기본 smoke와 명시적 ten-minute preset으로 구분하고 기존 raw/binary 복사 없이 새 빌드·전체 복구·무결성/의미 재검증을 제공한다. [실행 안내](../../diagnostic-tools.md)를 README·문서 지도·개발 기준·기존 네 진단 보고서에 연결했다. 공용 CI 스크립트에 Python 회귀 검사와 portable/native smoke를 포함했다.

로컬 portable/native CI와 새 네 suite smoke를 검증했다. 도구 편입 단계에서는 커밋·머지·push와 실제 GitHub CI 확인을 수행하지 않았다. 후속 사용자 요청으로 변경 검토·커밋·master 머지·push와 원격 CI 확인을 진행한다. 장시간 성능 실험을 재실행하지 않았으며 이전의 성능 미채택·원인 미확정·전원 장애 보류 판단을 유지한다.

## Validation and Acceptance

기존 var 없이 실제 CLI smoke가 동작하고 binary·입력·환경·명령·source hash와 원본을 보존해야 한다. 재집계/실패 판정은 실제 표본에서 다시 계산한다. hash 변조·재해시한 의미 변조·경로 이탈·부분 실패·취소/자식 합류·archive 손상을 검사한다. 관련 Go/Python 테스트와 기존 portable/native CI 스크립트를 실행한다. 새 성능 개선이나 실제 전원 장애 검증을 주장하지 않는다.

## Artifacts and Notes

보고서의 과거 명령은 당시 근거로 유지하고 새 안내 문서로 연결한다. 원본·실행 결과는 계속 Git 제외 var에 두며 도구·설정·문서만 포함한다.

## 실제 검증 기록

- `bash scripts/ci.sh portable`: Go 205개·20 package, Python 9개와 timer 네 조건·무결성·분석·출력 재사용 거절·재해시한 의미 변조 거절 통과. 근거 `var/ci/portable.ugErOu`, 로그 `var/repro-tools-ci-portable.log`.
- `bash scripts/ci.sh native`: Go race 237개·20 package, 명시된 child-helper skip 1개, Python 9개와 trace·C/OS I/O·거래/복구·archive smoke·동일 변조 검사 통과. 근거 `var/ci/native.Y1davb`, 로그 `var/repro-tools-ci-native.log`.
- Python 회귀는 일반/`-O` 실행 모두 9개 통과. hash/추가 파일/중복 JSON/경로 이탈·parent symlink·불완전 완료 표시·archive 손상·재해시 의미 모순·overlay source 불일치·비정상 trace·공급량/순번·OS 관측 불가를 검사했다. Go timerprobe 5개는 취소/worker 합류·partial 자료 보존·trace 켬/끔·출력 재사용·기간 입력을 검사했다.
- 수동 새 실행은 `var/repro-tools-timer-smoke-allowed`, `var/repro-tools-soak-smoke`, `var/repro-tools-trace-smoke`, `var/repro-tools-io-smoke`에 보존했다. 각 suite 실행/분석과 거래 전체 복구·중복 재시도·WAL 불변을 통과했다. I/O smoke는 압축 member hash 확인 뒤 새 원본만 prune하고도 검증·분석을 통과했다.
- 첫 timer smoke는 sandbox의 `ps` 거절로 실패했다. 실패 묶음 `var/repro-tools-timer-smoke`를 보존했고 `verify --allow-failed`는 `completed=false`로 확인했다. 허용된 관찰 권한에서 새 경로로 실행한 smoke는 통과했다.
- 기존 soak 6개는 summary·전체 관찰량·미전송 건수·5초 구간 CPU/GC/저장 단계가 이전 분석과 일치했다. 기존 trace 2개는 summary와 두 프로세스 trace hash·event/sync count·clock residual이 일치했다. 원본 manifest hash 검증을 포함했다.
- 문서 validator는 52개 문서의 경로·상대 링크·미치환 항목 검사를 통과했고 tracked/untracked 공백 검사도 통과했다. 실험 데이터는 Git 제외 상태다.
- 과거 timer/OS I/O 묶음도 두 타이머의 분포·실패와 거래 summary·51,178개 C 호출/50,928개 WAL 호출·OS record 집계가 일치했다. 최신 timer trace 대조의 네 조건은 분포·실패 건수·전체 실패 표본이 일치했다. 최종 분석은 `var/repro-tools-final-old-{soak,trace,io,timer}.json`에 입력 밖으로 저장했다.

## 후속 검토와 전달

- 재집계 CLI가 진단 입력 경로의 이름을 `loadgen`으로 가정하던 제약을 수정했다. 지정한 경로를 그대로 사용하며 native smoke에서 입력 디렉터리를 `renamed-input`으로 복사한 후 독립 재집계 성공을 검사한다.
- 후속 검토의 전체 portable/native CI 스크립트를 통과했다. portable Go 205개·native race 237개, 각 Python 9개와 실제 smoke를 확인했다. 근거는 `var/ci/portable.USmzGc`, `var/ci/native.aU628n`과 `var/repro-tools-release-{portable,native}.log`다. 장시간 실험·실제 전원 장애는 재실행하지 않았다.
- 첫 원격 CI 실행 42184471552는 portable 통과, native Go 237개·Python 9개 통과 후 호스트 분석에서 실패했다. 6열 고정 파서를 iostat 헤더 기반으로 바꾸고 CPU만·단일·복수 디스크와 열 불일치 회귀 검사를 추가했다. 디스크 미표시는 0으로 채우지 않고 unavailable/null로 기록한다. Python 일반/`-O` 회귀 10개 통과. 최종 원격 CI는 수정 커밋에서 다시 확인한다.
- 호스트 파서 보완 후 기존 trace 끔/켬과 I/O 거래의 호스트 record 수·CPU 중앙값·디스크 중앙값이 이전 분석과 일치했다. 로컬 재검증 근거는 `var/repro-tools-host-fix-{portable,native}.log`다.
