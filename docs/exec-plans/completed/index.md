# Completed execution plans

[운영 규칙](../index.md)에 따라 상태가 Completed인 계획만 등록한다. 완료일은 본문의 종료일과 일치해야 한다. 취소된 계획은 이 목록에 등록하지 않는다.

| 계획 | 완료일 | 결과 | 검증 근거와 한계 |
| --- | --- | --- | --- |
| [실험 자료 패키징·복원](20261112-evidence-package.md) | 2026-11-12 | 이전 실행·고정 빌드 수집·복원본 전용 검증/재집계와 비교표 재생성 | Python 53개·portable 215개·native race 249개·원본 비가용 smoke·115 entry 실제 사본 복원/비교 일치. dirty 원본 엄격 거절·동일 디스크·전원 장애 보류 |
| [프로젝트 결과 보고서와 문서 현행화](20261112-project-results.md) | 2026-11-12 | 구현/거래/복구·시점별 성능과 한계 종합, 주요 진입 문서 갱신 | tracked 수치/조건·기준 코드 원격 CI·링크/앵커·이전 로그 보존 확인. 문서만 변경·v1 Blocked 및 실제 전원 장애 미완료·보류 유지 |
| [실험 자료 보관·정리 미리보기](20261112-artifact-retention.md) | 2026-11-12 | var 용량·참조/해시·원본/고정 빌드 보호와 JSON/Markdown 삭제 후보 | 검토 수정 후 기존 4,798개 파일/429개 제어 해시 유지·Python 44개 통과. 구현 시 Go portable 215개·native race 249개·smoke 통과. 실제 삭제·전원 장애 미실행 |
| [수동 거래와 같은 ID 재시도 CLI](20261112-trading-cli.md) | 2026-11-12 | 계정/가격/수량 주문·취소·조회와 요청 파일 기반 같은 ID 결과 확인/재시도 | portable 215개·native race 249개·각 Python 32개·진단 smoke·실제 CLI/서버 재시작 통과. 전원 장애 보류 |
| [거래·복구 데모 확장](20261112-expanded-trading-demo.md) | 2026-11-12 | 거래 규칙·예약금 반환·ID 충돌과 WAL/snapshot/소유 서버 SIGKILL 복구를 한 명령으로 검증 | portable 210개·native race 243개·각 Python 32개·진단 smoke 통과. 이전 공간 부족 실패 보존·실제 전원 장애 보류 |
| [정상 세 조건 재측정](20261109-normal-capacity-recheck.md) | 2026-11-09 | 신규 2,000/3,000/s 각각 3/3·5,000/s 1/3, 누적 24회 보존 | 세 root inventory·raw 재집계·이전 실패/무효 포함. 누적 qualified는 2,000/s만·내재 최대/전원 장애 미검증 |
| [스냅샷 실패 구간과 정상 처리량](20261107-snapshot-window-capacity.md) | 2026-11-09 | master 반영/CI·snapshot 응답 tail/저장 분석·정상 관찰 채택점 2,000/s와 인접 3,000/s 미채택 확인 | 이전 중단/실패 포함 15개 판정·새 9개 봉인/무결성·source CI 통과. 병행 호스트 부하 관측·내재 최대/회귀 미판정·전원 장애 보류 |
| [낮은 요청률 기준과 정수 JSON 비교](20261106-lower-rate-baselines.md) | 2026-11-06 | 정상 10,000/s 3/3 채택·snapshot 1,000/s 2/3 미채택, JSON 할당 감소 후보 유지 | 후보 정상 3/3·원본/조건/자동 비교·Python 27개·portable 206개·native race 238개 통과. 인과 지연/최대 처리량 향상 미주장·전원 장애 보류 |
| [현재 기준 반복 측정](20261106-controlled-baseline.md) | 2026-11-06 | clean pin·환경 gate·6회 기준 재집계·실패 보존 | 정상 0/3·snapshot 2/3로 모두 미채택. Python 25개·portable 205개·native race 237개·실제 중단 확인. 전원 장애 보류 |
| [실험 결과 비교 자동화](20261106-experiment-comparison.md) | 2026-11-06 | 원본 재검증·기존 P6 판정 재사용·JSON/Markdown·조건/무효/중복 보존 | Python 17개·portable 205개·native race 237개·실제 비교 smoke·보관 자료 검증. 실제 개선 채택 없음·전원 장애 보류 |
| [재현 가능한 실험 도구](20261106-reproducible-diagnostics.md) | 2026-11-06 | run/analyze/verify·타이머/재집계 CLI·계측 overlay·명령/CI 연결 | portable 205개·native race 237개, Python 10개·네 suite smoke·과거 네 묶음 재분석 일치. 장시간 재실행·원격 CI·전원 장애는 미검증 |
| [타이머 trace 켬·끔 대조](20261106-timer-trace-control.md) | 2026-11-06 | 4조건 240,000개 검증, trace 끔에서도 50ms 한도 초과 확인 | 각 조건 1회·호스트 상태/UTC 변화 관찰, 인과 효과 미확정 |
| [타이머 분리·저장 OS I/O 관찰](20261103-timer-os-io.md) | 2026-11-03 | worker 한도 초과 재현, C 호출·프로세스 I/O 누계 관찰 | 실제 거래 전체 복구 일치, 커널 내부 추적 미확보·계측 영향 미확정 |
| [실행 trace·호스트 자원 진단](20261103-trace-host-diagnosis.md) | 2026-11-03 | 실행 대기·WAL sync 경계와 전체 복구 확인 | 끔/켬 각 600초, 50ms 미전송 미재현·호스트/시각 정렬 한계 |
| [진단 계측 10분 반복과 복구](20261103-diagnostic-soak-repeats.md) | 2026-11-03 | 끔/켬 각 3회·전체 복구 일치, 미전송 경계 분석 | 6회 Go/Python 원본 대조, 미전송 15건·원인 미확정·전원 장애 보류 |
| [지연 구간 분석과 생성기 진단](20261102-latency-diagnostics.md) | 2026-11-02 | 기존 240개 구간 대조·선택적 전달 경계/생성기 자원 계측·원본 검증 | portable 200개·native race 232개, 원인 미확정·새 10분 반복/계측 비용 미측정·전원 장애 보류 |
| [최적화 버전 10분 부하·복구 비교](20261102-optimized-ten-minute-comparison.md) | 2026-11-02 | 끔/켬 각각 600초·전체 복구·재시도 검증과 이전 원본 비교 완료 | 켬 CPU 18.8%·평균 생성 16.8%·복구 46.9% 감소. 전체 p99는 두 조건 모두 증가, 켬 미전송 9건·구간 기준 미달. 각 1회·전원 장애 보류 |
| [스냅샷 생성 중복 직렬화 제거](20261102-snapshot-size-preflight.md) | 2026-11-02 | 동일 상태 5쌍 생성 중앙값 29.7%·사전 계산 79.4% 감소, 후보 채택 | portable 195개·native race 227개, 차등/fuzz·파일 bytes·재시작·양방향 버전 복구 일치. 느린 후보 표본 1개 포함, 동시 부하·전원 장애 미검증 |
| [복구 디코더 최적화](20261030-snapshot-decoder.md) | 2026-10-30 | 검증 계약을 유지한 후보 채택, 동일 상태 5쌍 전체 복구 32.5%·decode 56.4% 감소 | portable 190개·native race 222개, 차등/기존 reader fuzz와 전체 상태 bytes 일치. 단일 frozen 상태·캐시 조건·전원 장애 보류 |
| [스냅샷 단계 계측](20261030-snapshot-stage-metrics.md) | 2026-10-30 | 동일 상태 5쌍 복구·생성 계측 및 병목 진단, 전체 상태 bytes 일치 | portable 187개·native race 219개 통과. 캐시/동시 부하·전원 장애 성능으로 확대하지 않음 |
| [스냅샷 유무 10분 부하·복구 비교](20261030-1627-ten-minute-comparison.md) | 2026-10-30 | 각 10분 실행·전체 상태/최초 결과·중복 재시도 복구 확인과 지연/자원 보고 | 복구 모두 일치. 끔 14건 미전송, 5초 p99 참조 기준은 두 조건 미달. 각 1회·전원 장애 보류 |
| [한 명령 거래 데모](20261030-1543-trading-demo.md) | 2026-10-30 | 실제 HTTP 거래·정상 재시작/WAL 복구·중복 재시도 한 명령 검증 | 실제 명령 2회, portable 185개·native race 217개 통과. 원격 CI·스냅샷·강제 종료·전원 장애 검증은 포함하지 않음 |
| [CI 자동 검증](20261030-1529-ci-automation.md) | 2026-10-30 | 일반/실제 저장 job, 공용 검사와 테스트 누락·skip 검증 구현 | 로컬 portable 180개·native race 212개 통과, Linux 교차 컴파일·actionlint 통과. 실제 GitHub 실행·보호 규칙과 전원 장애 검증은 포함하지 않음 |
