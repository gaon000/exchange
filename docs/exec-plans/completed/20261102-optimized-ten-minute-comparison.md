# 최적화 버전의 스냅샷 유무 10분 부하·복구 비교

상태: Completed
종료일: 2026-11-02

[ExecPlan 운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

최적화한 clean master `e5fb860`의 동일 바이너리로 스냅샷 끔/켬 각각 10분 부하와 정상 재시작·전체 상태 복구를 검증하고, [기존 10분 결과](../../benchmarks/soak-10m-snapshot-comparison.md)와 비교한다. 이번에는 decoder·크기 사전 계산 개선이 모두 포함된다. 최대 처리량 탐색·전원 장애 실험·추가 코드 최적화는 범위 밖이다.

## Progress

- [x] 2026-11-02: 이전 입력·실행기·복구 검증 절차와 clean 기준 commit 확인. feature branch 생성.
- [x] 2026-11-02: clean e5fb860 서버/생성기 동결, 환경·hash 기록. 6초 snapshot 예비 실행과 검증기 오류 검출 2개 통과. 이전 원본 234개 hash 검증.
- [x] 2026-11-02: 스냅샷 끔 600초·60,000건 실행, 고유 명령 51,385개 전체 조회·정상 재시작·12개 중복 재시도 및 상태/WAL 보존 검증 통과.
- [x] 2026-11-02: 30초 유휴 뒤 스냅샷 켬 600초 실행·전체 복구·재시도 검증 통과. 본 측정 9건 GENERATOR_LATE로 measurementValid=false, generator exit=1을 그대로 보존.
- [x] 2026-11-02: 이전/현재 네 실행의 Go 재집계 및 Python 독립 계산, 전체 상태·WAL·원본 hash 검증. 모든 5초/1분 결과와 차이를 보고서에 기록.

## Surprises & Discoveries

이전 끔 조건은 14개 생성기 미전송으로 measurementValid=false였다. 해당 실패도 원본 그대로 비교한다. 이전 조건은 전체 p99 외에 5초 구간별 20ms 참조값을 충족하지 못했다. 상태 크기·실제 실행 수가 다를 수 있으므로 고정 상태 5회 비교와 이번 동시 부하를 구분한다.

현재 끔은 미전송 0, 켬은 9개 GENERATOR_LATE다. 두 조건의 전체 요청 p99는 이전보다 높았으나 마지막 1분 p99와 초과 구간 수는 줄었다. 켬의 CPU·평균 생성 시간·복구 시간 감소를 전체 지연 기준 충족으로 해석할 수 없다. 켬의 미전송은 5분/7분 구간에 집중됐고 일부는 snapshot 호출 구간 밖에서 관찰돼 원인을 단정하지 않는다.

## Decision Log

- 2026-11-02: 기존 설정·실행기를 복사하고 변경 없이 사용한다. 초당 100건, 워밍업 5초+600초, seed 17, namespace soak-v1, 동시 요청 한도 2,048, deadline 5초·발사 지연 한도 50ms를 유지한다.
- 2026-11-02: 끔→켬 각 1회, 500ms 계측·스냅샷 5초, 프로파일러 끔. 첫 복구 종료 후 30초 이상 유휴. 같은 개발 장비의 다른 사용자 프로세스는 제어하지 않는다.
- 2026-11-02: 원본 8GiB·서버 RSS 2GiB·최소 여유 공간 12GiB를 기존과 동일하게 감시한다. 과거 실험을 삭제하지 않으며 상한 초과 시 소유 프로세스만 종료하고 실패를 보존한다.
- 2026-11-02: 두 600초 실행과 복구 검증은 완료했지만 성능 기준은 미달로 기록한다. 누락이 있는 실행을 재실행 성공으로 대체하지 않으며 추가 최적화나 원인 단정으로 범위를 확대하지 않는다.

## Outcomes & Retrospective

요청한 600초 부하 2회와 전체 복구·중복 재시도 검증을 완료했다. 현재 끔/켬은 고유 명령 51,385/51,379개, 주문 45,280/45,275개, 체결 12,117/12,115개와 모든 잔고가 재시작 전후 일치했다. 각각 12개 중복 재시도 뒤 전체 상태와 WAL hash도 유지됐다.

전체 요청 p99는 끔 9.207→12.028ms, 켬 17.556→20.593ms로 증가했다. 켬은 미전송 9건으로 measurementValid=false다. 마지막 1분 p99는 끔 12.382→6.203ms, 켬 38.309→24.334ms로 감소했다. 켬 서버 CPU 91.03→73.89 CPU초, snapshot 평균 401.130→333.814ms, 재시작 준비 4.713→2.504초다. 각 조건 1회·공유 호스트·상태 크기 차이의 한계를 유지하며 전체 지연·최대 처리량 개선 달성을 주장하지 않는다. 실제 전원 장애는 보류다. 커밋·병합·push는 하지 않았다.

## Context and Orientation

[기존 보고서](../../benchmarks/soak-10m-snapshot-comparison.md)는 `3b5a6f5` 기준이다. 현재 `e5fb860`은 단계 계측, decoder 개선, 생성 사전 계산 개선을 포함한다. [설정](../../../config/benchmarks/soak-10m-v1.json)과 [초기 잔고](../../../config/benchmarks/config-v1.json)는 동일하다. 기존 원본 `var/soak-comparison-20261030.92490s`의 run.py/analyze.go/compare.py/test_run.py를 새 원본 `var/soak-optimized-20261102.yawwm75d`에 복사한다.

## Plan of Work

소스 변경 전 clean 기준 서버·생성기 binary를 새 원본에 빌드하고 ready의 revision/modified와 hash를 확인한다. 6초 예비 실행 후 각 조건의 실제 HTTP 부하를 순차 수행한다. 부하 종료 뒤 응답받은 모든 고유 명령의 최초 결과·현재 주문과 모든 계정 잔고·호가·체결을 조회한다. 정상 재시작 후 먼저 전체 GET을 비교하고 기존 명령 12개의 재시도 후 전체 상태·WAL hash를 다시 비교한다. 원본 표본에서 일반·5초·1분 요약을 재집계하고 과거 수치와 함께 지연·누락·CPU/GC·메모리·스냅샷·복구 비용을 기록한다.

## Validation and Acceptance

두 조건 모두 600초 구간을 유지하고 누락·실패를 숨기지 않는다. 원본 응답 fingerprint·순번·자산 공급과 재시작 전후 전체 HTTP 파일 hash를 대조한다. Go 요약 재계산, Python 독립 분위수·구간 집계, 입력·바이너리·원본 hash를 확인한다. 새로운 결과는 각 조건 1회 관찰이며 qualified 처리량이나 SLA로 채택하지 않는다. 원본 측정과 무관한 테스트/프로파일러를 동시에 실행하지 않는다. 애플리케이션 소스를 변경하지 않으므로 이미 통과한 CI를 재실행하지 않고 문서·JSON·공백을 검증한다.

실제 검증: 6초 예비 실행·검증기 오류 검출 2개, Go 원본 재집계 4개, Python 242,000개 표본과 520개 구간 검증을 통과했다. 실험별 runId를 제외한 60,520개 요청 계획 hash가 네 실행 모두 같았다. 이전 234개 원본과 현재 binary/input hash를 재확인했다. 성능 판정과 별개로 두 현재 실행의 전체 HTTP 상태·최초 응답·WAL·정상 종료 검증은 PASSED다.

## Idempotence and Recovery

새 원본 폴더에서만 초기화·실행한다. 기존 run과 원본을 덮어쓰지 않는다. 실패·누락도 보고하며 성공 실행으로 대체하지 않는다. 종료·오류 처리는 실행기가 소유한 자식에게만 적용한다. 재시작에서 초기 잔고를 재지급하지 않는다.

## Artifacts and Notes

기존 [원본 보관 정보](../../benchmarks/20261030-soak-artifacts.json), [생성 최적화](../../benchmarks/snapshot-preflight-optimization.md)와 대조한다. 새 원본 경로·환경·모든 실행/분석 명령과 hash는 최종 보고서에 남긴다.

- [최종 비교 보고서](../../benchmarks/optimized-soak-10m-comparison.md)
- [기계 판독 비교](../../benchmarks/20261102-optimized-soak-comparison.json)
- [원본 보관 정보](../../benchmarks/20261102-optimized-soak-artifacts.json)

## Interfaces and Dependencies

API·저장 계약·production source/dependency 변경 없음. 설치된 Go와 Python 표준 라이브러리만 사용한다. 커밋·병합·push는 이번 요청에 포함하지 않는다.
