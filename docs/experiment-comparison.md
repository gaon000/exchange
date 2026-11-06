# 실험 결과 비교 보고서

[진단 도구](diagnostic-tools.md)의 `compare`는 보관한 기준·후보 원본을 다시 검증해 JSON과 Markdown 비교표를 생성한다. 서버·생성기를 실행하지 않고 현재 source로 빌드한 판정 도구를 사용한다. 보관 바이너리는 실행하지 않는다.

## 실행

저장소 루트에서 [개발 도구](development.md)를 준비한 뒤 실행한다. Go 1.27.3 wrapper와 Python 3.9 이상 표준 라이브러리만 필요하다.

```sh
python3 tools/diagnostics/experiment.py compare --spec config/comparisons/soak-before-after-v1.json --output var/comparisons/soak-before-after-v1
python3 tools/diagnostics/experiment.py verify --bundle var/comparisons/soak-before-after-v1 --output var/comparisons/soak-before-after-v1-verified.json
```

[예제 명세](../config/comparisons/soak-before-after-v1.json)는 2026-10-30·2026-11-02의 기존 10분 실험을 끔/켬별로 비교한다. 이 보관 자료는 Git 제외 `var/`에 있어 새 checkout에 포함되지 않는다. 해당 자료가 없는 환경에서는 입력 오류를 보고서에 남기며 성능 개선으로 판정하지 않는다. 새 실험 묶음 경로로 명세를 바꾸어 사용한다. 출력은 매번 새 이름을 사용하며 parent는 자동 생성한다.

## 비교 명세

```json
{
  "version": 1,
  "criteria": "../benchmarks/criteria-v1.json",
  "comparisons": [
    {
      "name": "같은 조건의 기준과 후보",
      "baseline": [{"bundle": "../../var/baseline", "case": "off-1"}],
      "candidate": [{"bundle": "../../var/candidate", "case": "off-1"}]
    }
  ]
}
```

`bundle`과 선택적인 `criteria` 경로는 **명세 파일의 디렉터리 기준**이다. 위 예시는 `config/comparisons/`에 명세를 둘 때의 경로다. 비교는 1~20개, 기준·후보 배열은 각각 1~100개, 전체 입력 합계는 100개 이하로 제한한다. 이름은 중복할 수 없고 알 수 없는 명세 필드는 거절한다.

- 기존 `cmd/experiment`의 P6 실험: `bundle`에 개별 실행 디렉터리를 지정하고 `case`를 생략한다. 해당 디렉터리에 `loadgen/manifest.json`, 실험 manifest와 환경 기록 등이 있어야 한다.
- 새 진단 도구와 과거 10분/타이머 묶음: `bundle`은 전체 inventory가 있는 parent, `case`는 바로 아래 실행 디렉터리 이름이다. `case`에 상위 경로나 여러 단계 경로를 넣을 수 없다.
- 반복은 배열에 각 실행을 추가한다. 복사한 디렉터리나 symlink 별칭을 독립 반복으로 세지 않는다. 거래는 run ID, 타이머는 metadata와 표본 지문으로 중복을 확인한다.

P6 기준은 기본 [criteria-v1.json](../config/benchmarks/criteria-v1.json)이며 명세에서 다른 파일을 지정할 수 있다. 비교 v1은 version 1·5초 창·최소 3~100회 기준을 요구한다. 실제 P6 채택 판정은 [benchreport](../tools/benchreport/main.go)의 기존 검증·판정·반복 그룹 결과를 그대로 사용한다. 비교 코드가 별도의 완화된 합격 기준을 만들지 않는다.

## 보고서와 해석

새 출력 디렉터리에는 다음을 기록한다. 모든 입력 묶음 밖이어야 하며 symlink 별칭도 확인한다. 기존 출력은 빈 디렉터리라도 거절한다.

| 파일 | 내용 |
| --- | --- |
| `report.json` | 입력·조건·빌드·원본 inventory/완료 hash, 원본 재계산과 기존 판정, 전체 5초 창, 비교 통계와 판정 제한 |
| `report.md` | 같은 JSON에서 만든 지표 표·입력별 유효성·무결성 형식·조건/빌드 차이 |
| `manifest.json`, `completed.json` | 보고서 두 파일의 크기/SHA-256과 manifest 완료 hash |
| 실패 시 `failed.json` | 출력 과정의 오류. 완료 표시를 만들지 않으며 생성된 부분 파일은 보존 |

비교 지표는 전체 요청/예정/고유 신규 승인 p99, 5초 창의 최악 p99와 참조 상한 초과 수, 미전송, 신규 승인 처리량, 서버 CPU·peak RSS, snapshot 완료 수/호출 경과와 정상 재시작 시간을 포함한다. 타이머는 관찰/worker p99와 실패 수를 비교한다. 값이 없으면 JSON null과 Markdown —로 남긴다. 원본 무결성/의미 검사에 실패한 입력도 사유와 함께 보존한다.

각 표의 값은 **실행별 관찰값의 중앙값 [최소~최대; 관찰 실행 수]**다. 여러 실행의 요청을 합쳐 p99를 다시 계산하지 않는다. 원본 재계산을 통과한 무효 측정도 기술 통계에 포함하며 입력 표에 `measurementValid=false`와 사유를 남긴다. 중복으로 입력한 실행도 입력 표에서 삭제하지 않으며 중복 판정으로 검토를 차단한다. 따라서 중복을 넣은 기술 통계를 독립 반복의 통계로 해석하지 않는다.

차이는 후보−기준이고 변화율은 그 차이/기준 절댓값이다. 기준이 0이거나 어느 쪽 관찰이 없으면 변화율을 계산하지 않는다. 비율에 성능 개선/악화라는 자동 해석을 붙이지 않는다.

관측 범위는 구분한다. 지연·신규 승인 처리량은 본 측정 cohort, 서버 CPU 증분·peak RSS·snapshot 이벤트는 프로세스 생애, 복구는 별도 정상 재시작→ready/health다. snapshot 호출 경과는 기록된 UTC 차이이며 내부 단계 시간이나 단조 시각 비용이 아니다. UTC 음수 경과가 있으면 해당 중앙값은 unavailable로 남긴다. snapshot 이벤트가 없는 입력의 생성 비용을 0으로 추정하지 않는다.

## 판정 제한

`eligibleForReview=true`는 다음 조건을 모두 만족한 **후속 검토 가능 상태**다. `improvementClaimed`는 항상 false이며 인과 효과·통계적 유의성·최대 지속 처리량의 향상을 자동 선언하지 않는다.

- 양쪽 모두 원본 검증과 측정 유효성을 만족한다.
- 각 측의 실행들이 하나의 P6 benchreport series로 묶여 최소 반복을 채택했다.
- 각 측 내부의 기록된 조건·빌드가 같고 양쪽 조건이 일치한다. revision/바이너리 차이는 별도로 표시한다.
- run ID 중복·양쪽 공유가 없으며 필요한 하드웨어·저장 환경 기록이 있다.

부하·기간·계정/잔고·seed·실행 한도·HTTP/runtime·계측·snapshot·trace·profile·toolchain·기록된 환경 차이를 비교한다. 환경의 기록된 필드가 같아도 호스트 부하·온도·캐시까지 통제됐다는 뜻은 아니다. 예전 형식에서 기록하지 않은 하드웨어·저장 환경은 추정하지 않는다.

새 진단·타이머·비표준 10분 묶음은 원본/복구를 검증하고 수치를 비교해도 P6 baseline으로 채택하지 않는다. 양쪽 모두 통과한 P6 비교도 수동 검토가 필요하다. `criteriaDecision`, `criteriaGroups`, `judgement.blockers`로 기존 판정과 비교 제한을 확인한다.

CLI 종료 0과 `status=reported`는 보고서 생성 성공을 뜻한다. 입력 오류·무효·반복 부족·조건 불일치가 있더라도 보고서를 생성할 수 있다. 자동 판정 소비자는 완료 hash와 JSON의 각 검토 상태를 확인해야 한다. 잘못된 명세·빌드/판정 도구 실행 실패·출력 실패는 종료 코드 1이다.

## 검증

```sh
python3 -m unittest discover -s tools/diagnostics -p 'test_*.py'
python3 -O -m unittest discover -s tools/diagnostics -p 'test_*.py'
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

회귀 검사는 반복·중복·조건/빌드 차이·무효 관찰·없는 수치·Markdown escape·원본 밖 출력·재사용·부분 쓰기 실패를 확인한다. 공용 CI smoke는 새 timer/거래 묶음으로 실제 compare와 보고서 hash 검증을 수행하고 같은 run의 양쪽 재사용을 차단한다. 원본과 완료 hash를 모두 다시 계산한 의미 변조도 비교 입력 오류로 남기는지 검사한다.

기존 보관 10분 자료·6회 진단·타이머·P6 정상/포화 표본으로 별도 검증한다. 장시간 부하와 실제 전원 장애를 다시 실행하는 명령이 아니다. 전원 장애 검증은 계속 보류다.
