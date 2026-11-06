# 호스트 조건을 기록한 기준 반복 측정

상태: Completed
종료일: 2026-11-06

[운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

비교 도구를 master에 반영한 뒤 clean ca74b53 생산 바이너리로 현재 기준을 다시 확인한다. 기존 P6의 warmup 5초·측정 20초·p99 20ms·5초 창·3회 반복·오류/누락 0 기준을 유지한다. 정상 29,000/s, snapshot 3,000/s를 trace/profile/생성기 진단 없이 측정하며 호스트 유휴 확인·전원/thermal 기록·순서 교차·cooldown으로 실행 조건을 명시한다. 전원 장애와 10분 실험은 수행하지 않는다.

## Progress

- [x] 비교 보고서 ca74b53 커밋·master 머지/push, clean 생산 바이너리 고정.
- [x] 원격 CI 확인과 호스트 기록·quiet gate·예산·실패 보존 실행 도구.
- [x] 짧은 사전 확인 뒤 정상/snapshot 각 3회 실행·기존 판정 재집계.
- [x] 원본·보고서·변동 범위·한계 문서화와 회귀 검증.

## Surprises & Discoveries

개발 컴퓨터의 타 앱·온도·스케줄러를 완전히 격리할 수 없다. 다른 앱을 강제로 종료하거나 OS 설정을 변경하지 않는다. 시작 전 10초 호스트 busy 중앙값 15% 이하·최대 30% 이하를 요구하고 미달은 자료로 보존한다. 측정 중 호스트 관찰 비용도 0으로 가정하지 않는다.

## Decision Log

- 2026-11-06: source가 clean인 ca74b53에서 exchange/loadgen/experiment/benchreport/storageprobe를 먼저 빌드하고 Go VCS modified=false와 SHA-256을 고정했다. 이후 도구/문서 변경과 생산 바이너리 pin을 구분한다.
- 2026-11-06: 기존 cmd/experiment와 benchreport를 사용하고 호스트 관찰·예산·고정 순서만 표준 Python 실행기로 제공한다. production dependency와 거래/저장 계약 변경 없음.
- 2026-11-06: P6 판정 실패도 반복 목록에 보존하며 결과를 본 뒤 상한을 완화하거나 실패 반복을 교체하지 않는다. 여유 공간 8GiB·새 묶음 4GiB·단일 자식 RSS 2GiB·소유 프로세스 합계 RSS 3GiB를 감시한다.

## Outcomes & Retrospective

비교 보고서 ca74b53의 master 반영과 실제 GitHub CI 성공을 확인했다. 유휴 gate·전원/thermal·소유 자식 RSS/디스크·실패/신호 보존·clean build freeze를 명령으로 정리했다. 초기에 strict smoke 3개 묶음의 9개 유휴 관측이 실패했고, 사용자가 다른 작업을 줄이겠다고 응답한 뒤 본 측정의 10초 gate를 통과해 6개 실행을 모두 수집했다.

정상 29,000/s는 0/3, snapshot 3,000/s는 2/3 통과로 두 그룹 모두 qualified=false다. snapshot2의 마지막 5초 창 요청 20.628375ms·예정 20.685959ms가 20ms 한도를 넘었다. 새 유효 처리량 기준을 확보했다고 주장하지 않는다. 측정 작업의 완료와 성능 채택을 구분한다. [결과](../../benchmarks/controlled-baseline.md)와 [재현 명령](../../controlled-baseline.md)을 기록했다. 다음은 더 낮은 요청률의 별도 사전 정의 그룹을 검증하는 작업이다. 실제 전원 장애는 보류다.

- 2026-11-06: 실행 경로만 확인하는 6초 smoke는 유휴 실패를 그대로 기록하고 gate enforced=false로 동작하도록 분리한다. 실제 reference는 사전의 10개 관측·15%/30% 한도를 그대로 강제하며 어떤 실패 반복도 교체하지 않는다.

## 실제 검증

- ca74b53의 원격 CI 42190355036은 portable/native 모두 success다. 현재 기준 도구·결과의 원격 CI와 혼합하지 않는다.
- 실제 경로 smoke 두 조건 모두 종료 0·원본/판정 기록을 통과했다. 6초 smoke는 P6 채택 조건이 아니다. `var/controlled-baseline-path-smoke`에 보존했다.
- 실제 reference는 `var/controlled-baseline-reference`, 생산 빌드는 `var/controlled-baseline-build-ca74b53`다. 모든 생산 binary hash와 modified=false를 확인했고 입력·환경·실행 인자·원본·실패·호스트 관찰과 전체 완료 hash를 보존했다.
- 최초 qualification과 현재 source의 별도 benchreport 재실행 `var/controlled-baseline-recheck.json`의 6개 cases/2개 groups가 완전히 일치했다. reference/path smoke 전체 inventory와 최초 세 실패 묶음의 completed=false를 확인했다.
- Python 일반/`-O` 각각 25개. quiet 미달 reference가 부하를 시작하지 않음, 소유 트리 구분, 자원 한도, dirty/변조 build 거절, 핸들러 복원·강제 종료 실패·부분 자료 보존을 검사했다.
- 실제 SIGTERM을 유휴 수집 중 전달해 종료 1·소유 sampler 합류·부하 미시작·incomplete 자료 보존을 확인했다. `var/controlled-baseline-cancel-check`에 보존했다.
- 전체 portable Go 205개·native race 237개·각 Python 25개와 실제 거래/복구/비교/변조 smoke가 통과했다. 로그는 `var/controlled-baseline-ci-{portable,native}.log`, 근거는 `var/ci/portable.EqdKfi`, `var/ci/native.U2jXif`다. 성능 실행이 모두 끝난 뒤 검사했다.
- [정규화 결과](../../benchmarks/20261106-controlled-baseline.json)에 전체 표본 판정·모든 창·source/원본 hash·호스트/예산 관측을 기록했다. 새 테스트 데이터는 Git 제외 상태다.
