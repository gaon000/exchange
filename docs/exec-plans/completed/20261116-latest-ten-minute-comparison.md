# 최신 master 10분 비교 준비

상태: Completed
종료일: 2026-11-16
시작일: 2026-11-16

## Progress

- [x] 공간 확보 확인(약29GiB), 최신 master e258b8f, 동일 입력·원본 보존 확인.
- [x] clean e258b8f binary/6초 두 조건·스냅샷 생성·전체 복구 확인.
- [x] 끔600초·고유51,383명령·전체복구·12재시도 PASSED. 본 측정 GENERATOR_LATE3건으로 measurementValid=false, 원본 보존.
- [x] 켬600초·고유51,378명령·전체복구/12재시도 PASSED. 미전송11·전송 오류1·UNKNOWN1은 보존.
- [x] 전체 최초 결과·주문/잔고·공급량·호가/체결·재시작·12개 재시도·WAL 불변.
- [x] 이전 두 캠페인과 원본 재집계/JSON·Markdown·문서 갱신.

## Context and Acceptance

최신 master e258b8f를 변경 없이 사용한다. 입력은 config/benchmarks/config-v1.json·soak-10m-v1.json이며 namespace soak-v1·seed17·준비20건·워밍업500건·본측정60,000건이다. 이전2026-10-30 및2026-11-02의 두 캠페인(각 끔/켬)을 원본 검증 후 비교한다. 원본 보관경로는 var/diagnostics/soak-latest-10m-20261116, 부가 장비/외부공간 감시는 var/soak-latest-20261116이다.

지연/요청률은 본 측정 cohort, CPU·GC는 측정 시작/끝 직전 관측 차분, 생애 peak RSS와 GC 시점 live heap은 별도 계수다. 완료 snapshot 중 측정과 겹친 event의 호출 비용을 집계한다. 재시작은 process 시작→ready/health이며 전체 최초 결과·현재 주문·4계정 잔고/공급량·호가·모든 체결 페이지를 먼저 대조한 뒤12개 기존 명령을 재시도한다. 느린 표본과 무효 측정을 보존하고 성능 채택/인과 효과를 주장하지 않는다.

## Surprises & Discoveries

새 비교에서 이전과 생성기 진단 간격0/500ms가 다름을 확인했다. 원본 조건 차이를 감추지 않고 자동 비교에서 차단한다.

기존 CLI의 내장 free guard는8GiB여서 외부 5초 감시로 이전 실험의12GiB 여유를 추가 유지한다. 원본/고정 빌드는 clean master에서 만들고 실행기/생산 source는 변경하지 않는다.

## Decision Log

각 조건1회·100requests/s·동일seed17·동시요청2048·deadline5초·발사50ms·500ms계측·snapshot5초·profiler/trace끔. 본 측정 중 별도 테스트/재집계 없음. 유휴 CPU gate는 이전10분과 동일하게 적용하지 않고 coarse 호스트 관측을 별도 기록한다. 요청 누락/실패를 보존하며 성능 합격과 자료 수집/복구 통과를 구분한다.

## Outcomes & Retrospective

두600초 실행·정상 재시작·응답받은 모든 명령의 최초 결과/주문/잔고/호가/체결·12개 중복 재시도와 WAL 불변 검증을 마쳤다. 생성기 미전송 끔3/켬11건으로 두 성능 측정은 무효다. 켬에는 전송 오류/UNKNOWN1건이 있고 복사한 snapshot/WAL로 복구 후 GET한 결과404 UNKNOWN이었다. 새 주문은 제출하지 않고 원본/사본 SHA를 유지했다.

11/02 대비 p99는12.028→7.443/20.593→18.498ms지만 켬의5초 초과 구간은23→26개·마지막1분 p99는24.334→33.882ms다. snapshot 평균333.814→238.370ms·복구0.470/1.604초를 관찰했으나 생성기 진단0→500ms와 단일 실행/공유 호스트로 인과 효과·성능 채택을 주장하지 않는다.

최신193개 봉인 파일·이전234/261개 legacy 파일,6개 원본의Go 재계산·최신 독립 Python 전체/창별 계산·계획60,520개6실행 일치를 확인했다. 새 폴더의 JSON/Markdown 재집계가 bytes까지 같다. 과거10/30켬UTC관측 순서 역전1건은 측정범위 자원만unavailable로 남겼다. 실제측정 중 최소 여유24.956GiB다. [최종 보고서](../../benchmarks/latest-soak-10m-comparison.md)와 JSON에 근거를 보존한다.

문서/JSON만 변경했고 생산/도구 source는 그대로다. 신규 부하·복구·원본/계산 검증을 수행했으며 같은 master CI는 반복하지 않았다. 위 완료 기록은 측정·분석 시점이며 이후 커밋/병합/푸시·원격 CI는 별도 발행 단계에서 확인한다. 실제 전원 장애는 미완료·보류, v1 Blocked 유지.
