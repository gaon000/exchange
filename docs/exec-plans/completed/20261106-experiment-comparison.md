# 실험 결과 비교 보고서 자동화

상태: Completed
종료일: 2026-11-06

[운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

현재 checkout의 도구로 기존 보관 자료를 다시 검증해 기준·후보 조건, 유효성, 전체/구간 지연, 자원·스냅샷·복구 관찰값을 JSON과 Markdown으로 함께 생성한다. 기존 benchreport의 P6 채택 판정을 재사용하고 진단 묶음을 P6 기준으로 채택하지 않는다. 원본을 변경하거나 장시간 부하·전원 장애 실험을 다시 수행하지 않는다.

## Progress

- [x] clean master aa9f3c1, 기존 판정 도구·보관 형식 확인과 feature branch 생성.
- [x] 비교 명세·입력 adapter·조건/유효성/중복/반복 판정 구현.
- [x] JSON·Markdown 출력과 실패/변조/경로/재사용·CI 회귀 검증.
- [x] 실제 보관 자료 비교·문서/검증 기록·계획 마무리.

## Surprises & Discoveries

기존 P6 실험과 새 진단 묶음의 완료/manifest 형식이 다르다. 예전 10분 묶음은 진단 파일이 없고 환경 정보가 제한적이다. 기록하지 않은 환경·관측치는 추정하거나 0으로 채우지 않는다.

## Decision Log

- 2026-11-06: compare --spec JSON --output 새디렉터리를 추가한다. 명세 경로는 명세 파일 기준이며 comparison별 baseline/candidate 반복 배열에 bundle과 선택 case를 지정한다.
- 2026-11-06: 현재 source의 benchreport/experimentcheck를 빌드해 사용하며 보관 바이너리를 실행하지 않는다. 실패·무효·비교 불가도 행을 보존하고 개선 채택과 구분한다. 원본 밖 새 디렉터리에 report.json/report.md와 완료 hash를 기록한다.
- 2026-11-06: production dependency 추가·거래 계약 변경·commit/push는 이번 구현 요청에 포함하지 않는다.

## Outcomes & Retrospective

비교 명세 기반 CLI, P6/보관 거래/타이머 adapter, 기존 판정의 재사용과 JSON/Markdown 생성을 구현했다. 조건/빌드/환경·무효·중복·독립 반복 부족을 기록하고 개선 판정은 자동 선언하지 않는다. 입력 오류도 행을 보존한다. 보고서는 입력 밖 새 디렉터리에 두며 두 파일과 manifest의 hash 완료 표시를 제공한다.

실제 전원 장애와 장시간 부하를 재실행하지 않았고 기존 성능 미채택 판단을 바꾸지 않는다. 구현 후 커밋·머지·push·원격 CI는 이번 요청에 포함하지 않는다.

## Validation and Acceptance

기존 보관 원본으로 정상·무효·조건 불일치·반복 부족과 원본 해시 보존을 확인한다. 합성 회귀로 같은 run의 복제/재사용, rehash 의미 변조, 기존 출력·symlink alias·부분 쓰기 실패를 검사한다. Markdown 수치와 JSON을 대조하고 기존 portable/native CI smoke에 연결한다.

## 실제 검증 기록

- Python 일반/`-O` 실행 각각 17개 통과. 독립 반복과 기존 P6 qualified 결과를 전제로 한 검토 가능 경로, 무효 수치 보존, 0/관측 없음, 중복·공유 run, 혼합 빌드/series·조건 차이·환경 부족, Markdown escape, 상대 경로·기존/alias 출력·부분 쓰기·불완전 원본을 확인했다.
- `bash scripts/ci.sh portable`: Go 205개·20 package, Python 17개와 실제 timer 비교·JSON/Markdown·완료 hash·같은 run 양쪽 재사용 차단·재해시 의미 변조 보고를 통과했다. `var/comparison-ci-portable.log`에 보존했다.
- `bash scripts/ci.sh native`: Go race 237개·20 package, 명시된 child-helper skip 1개, Python 17개와 실제 HTTP/APFS·전체 복구·trace/C/OS I/O·archive·거래 비교/중복/변조 smoke를 통과했다. `var/comparison-ci-native.log`에 보존했다.
- 최종 Python 비교 코드의 양 모드 smoke를 `var/comparison-final-smoke-{portable,native}.log`로 추가 확인했다. Go 판정 도구 source는 변경하지 않았다.
- 기존 10분 자료 네 실행은 모두 원본/복구를 재검증했고 미전송 14→0(끔), 0→9(켬)를 유지했다. 생성/CPU 관측은 프로세스 생애로 명시하며 이전 보고서의 측정 cohort와 혼합하지 않는다. 각 조건 1회·불완전한 환경·비표준 묶음·무효 측정 때문에 개선 판정을 차단한다.
- 기존 6회 진단, trace 끔 타이머 두 조건, P6 정상/snapshot smoke, 초기 100/s와 70,000/s 원본, 의도적으로 없는 경로를 포함한 5개 비교를 생성했다. 무효 P6 포화는 기존 판정·재계산한 수치를 남기고 복구 미관측을 null로 유지했다. 타이머 worker 조건의 실패와 조건 불일치, 누락 원본 오류를 보존했다.
- 기존 4개 parent inventory의 234/261/267/50개 파일을 사후 재검증했고 P6 sealed 입력의 완료 hash도 일치했다. JSON과 같은 함수에서 생성한 Markdown byte 일치와 보고서 완료 hash를 확인했다.
- 기존 자료의 실제 qualified 성능 개선 비교는 확보하지 않았다. 합성 회귀의 검토 가능 경로와 실제 무효/제한 경로를 구분한다. 모든 실제 비교의 개선 채택은 차단된 결과다.

## 산출물

- 명세 예제: [soak-before-after-v1.json](../../../config/comparisons/soak-before-after-v1.json).
- 명령·형식·관측 범위: [비교 보고서 안내](../../experiment-comparison.md).
- 실제 보고서: `var/comparison-reports/soak-before-after-v1/report.json`, `report.md`와 `var/comparison-reports/retained-adapters-v1/report.json`, `report.md`. 대용량 원본·환경별 출력은 Git 제외 상태다.

## 후속 전달

사용자의 후속 머지 요청에 따라 변경 검토·Python 회귀 재검증 후 커밋·master 반영·push와 원격 CI 확인을 진행한다. 다음 작업은 별도 계획의 trace 비활성 기준 반복 측정이다.
