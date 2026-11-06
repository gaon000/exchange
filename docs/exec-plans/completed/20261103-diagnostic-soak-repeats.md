# 진단 계측을 켠 10분 부하 반복과 복구 검증

상태: Completed
종료일: 2026-11-03

[ExecPlan 운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

스냅샷 끔/켬을 같은 진단 설정에서 각각 600초 3회 실행하고, 미전송과 응답 지연이 나타나는 경계를 비교한다. 매 실행 전체 HTTP 상태·최초 결과·중복 재시도와 재시작 복구를 검증한다. 엔진 최적화, 실제 전원 장애, 커밋·병합·push는 이번 범위에 포함하지 않는다.

## Progress

- [x] 2026-11-03: clean master `74a1206` 확인, feature branch 생성, 기준 문서·기존 harness·디스크 여유 확인. 문서 변경 전 동일 소스의 서버/생성기 빌드.
- [x] 2026-11-03: 6초 예비 실행의 전체 복구·재시도, 원본 변조/잔고·순번·최초 결과 오류 검출 2개 테스트, 재해시한 진단 변조 거절 2건, 압축 모든 member 검증 통과.
- [x] 2026-11-03: 총 6회 각 600초 실행, 매회 전체 복구·최초 결과·12개 재시도·WAL 불변 일치. 미전송 8/1/3/3/0/0건을 보존.
- [x] 2026-11-03: Go 원본/진단 의미 검증, Python 전체 요약 12개·5초 720개·1분 60개 대조와 histogram 구간 합계 일치. 여섯 archive 모든 member 사후 재검증, 이전 원본 261개 hash 불변 확인. 문서 44개/저장소 파일 241개 진단 0, 새 파일 공백 검사 통과. 분석 재실행 bytes 일치와 새 원본 267개 해시 manifest 기록.

## Surprises & Discoveries

시작 시 여유 공간은 약 13GiB다. 이전 켬 실행의 snapshot 원본만 약 3GiB였으므로 여섯 실행을 모두 압축 없이 보관하기 어렵다. 기존 실험은 변경하지 않고 새 실행에서 생성한 snapshot·전체 HTTP 조회 파일을 복구 검증 후 압축한다. 압축본 모든 member의 bytes·SHA-256을 대조한 뒤 해당 새 원본을 정리한다. 원본 복원이 가능한 archive와 파일별 manifest를 보존한다.

## Decision Log

- 2026-11-03: 입력은 기존 config-v1/soak-10m-v1 그대로, 100 requests/s, warmup 5초, 측정 600초, seed 17, 발사 지연 한도 50ms. 서버·생성기 계측 각 500ms, snapshot 주기 5초, profiler 끔.
- 2026-11-03: 쌍별 순서는 끔→켬 / 켬→끔 / 끔→켬. 모든 실행과 사후 검증·압축을 순차 수행하고 다음 실행 전 30초 유휴. 실패한 실행도 보존하며 유리한 결과로 교체하지 않는다.
- 2026-11-03: 서버 RSS 2GiB, 묶음 8GiB 한도를 유지하고 최소 여유 공간은 이번 보관 방식에 맞춰 8GiB로 명시한다. 부하 중 5초 간격으로 감시한다. 압축·재집계·테스트는 측정과 겹치지 않는다.
- 2026-11-03: 기존 무진단 실행과 직접 개선율을 주장하지 않는다. 지연 계측은 관찰 경계를 좁히며 인과관계·최대 처리량을 증명하지 않는다.

## Outcomes & Retrospective

여섯 실행 모두 전체 복구가 일치했다. 미전송은 scheduler 8건·worker 7건으로 구분됐다. 생성기 해당 관찰 구간의 GC 횟수 증가는 0이었고 근본 원인은 미확정이다. 요청 p99 중앙값은 끔 7.284ms·켬 16.047ms이며 켬 세 실행 모두 5초 구간 지연 초과가 남았다. 자세한 값과 한계는 [결과 보고서](../../benchmarks/diagnostic-soak-repeats.md)에 기록했다. 환경 sysctl 읽기는 sandbox에서 거절되어 승인 환경에서 off-1 초반에 재수집했다. 전체 호스트 경쟁·절대 시각 오차·진단 비용은 한계로 남는다. 실제 전원 장애는 보류이며 커밋·병합·push는 하지 않았다.

## Context and Orientation

[이전 지연 분석](../../benchmarks/latency-diagnostics.md)은 끔의 서버 저장 지연과 켬의 생성기 미전송을 확인했으나 단일 원인을 확정하지 않았다. 이번에는 `--diagnostics-interval 500ms`로 scheduler 전달 전/worker 첫 실행과 생성기 CPU·GC를 수집한다. [측정 계약](../../design-docs/benchmark-v1.md)을 따른다.

## Plan of Work

Git 제외 고유 parent `var/diagnostic-soak-20261103.fz83p2f4`에 기존 harness를 복사해 선택적 진단·실행 이름·8GiB 감시를 연결한다. 동일 commit의 binary를 고정하고 설정/소스/환경 hash를 보존한다. 짧은 실제 HTTP/APFS 예비 실행 후 여섯 측정을 수행한다. 전체 조회를 재시도 전후 비교하며 기존 명령 재시도 뒤 WAL 불변도 확인한다. 원본·진단 의미 검증과 Go/Python 재집계를 대조하고 각 실행 및 조건별 중앙값/범위를 기록한다.

## Validation and Acceptance

예비 실행에서 복구 검증기의 잔고/순번/최초 결과 오류 검출, 원본 hash 변조 거절과 진단 스트림 검증을 수행한다. 각 실행의 모든 예정 표본, 120개 5초 구간, 최초 결과·주문·잔고·전체 체결·호가를 검사한다. 일부 미전송은 측정 무효로 그대로 남기고 복구 결과와 구분한다. 압축본은 모든 member hash를 대조하고 과거 raw는 읽기만 한다. 문서만 추적 변경하므로 애플리케이션 전체 CI 재실행 대신 실험 도구 검증과 문서 검사를 한다.

## Idempotence and Recovery

새 case와 산출물은 exclusive create로 만들고 기존 폴더 재사용을 거절한다. 소유 자식만 정상 종료하고 종료·포트 해제를 확인한다. 중단되면 부분 원본과 실패 사유를 보존한다. 압축 검증 실패 시 원본은 제거하지 않는다.

## Artifacts and Notes

실행·복구·분석·압축 manifest와 모든 실패는 고유 parent에 보존했다. [기계 판독 결과](../../benchmarks/20261103-diagnostic-soak-report.json)와 로컬 artifact-manifest.json으로 원본을 식별한다. Git에는 집계 JSON과 보고서·계획만 기록하고 원본 대용량 파일을 올리지 않는다.

## Interfaces and Dependencies

production source·dependency·거래 API·WAL·snapshot 계약 변경 없음. task-local Go/Python harness만 조정한다.
