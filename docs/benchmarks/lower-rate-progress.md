# 낮은 요청률 기준 확보 진행 기록

상태: 사용자 재개 요청 뒤 6회 수집과 원본 검증 완료. 정상 10,000/s는 3/3 통과해 후속 작업점으로 채택했고 snapshot 1,000/s는 2/3 통과로 미채택이다. 정수 JSON 후보의 정상 조건 비교를 진행 중이다.

아래는 재개 전 중단 시점의 보존 기록이다. 당시 2026-11-06의 [사전 계획](../exec-plans/active/20261106-lower-rate-baselines.md)에 따라 정상 10,000/s·snapshot 1,000/s 각 3회를 목표로 시작했다. **현재 완료한 실행은 두 개이며, 어떤 조건도 기준으로 채택하지 않았다.** 생산 코드 최적화와 전체 경로 전후 비교도 아직 수행하지 않았다.

[기계 판독 기록](20261106-lower-rate-progress.json)에 원본 재검증·완료 실행·대기 관측·단위 후보 결과를 보존한다. 기존 기준 측정 도구 c9a9950의 master 반영과 [실제 CI](https://github.com/gaon000/exchange/actions/runs/42218285157)는 완료했다. 요청률 설정화·원본 보존 재개·대기/조건 선택은 feature branch에 기록했다.

## 완료한 실행과 남은 실행

clean 생산 빌드 086c61a를 고정했다. 기존 seed·초기 상태·warmup 5초/측정 20초·p99 20ms/5초 창·오류/누락 0·host gate를 유지했다. 실제 요청률은 protocol/order와 각 workload/manifest에서 확인했다.

| 실행 | 측정 유효 | 본 측정 요청 p99 | 기존 판정 |
| --- | --- | ---: | --- |
| 정상1, 10,000/s | true | 7.187708ms | 단일 실행 통과·반복 부족 |
| snapshot1, 1,000/s | true | 10.799209ms | WINDOW_3_P99 실패 |

두 실행의 미전송은 없었지만 snapshot1은 마지막 5초 창 기준으로 실패했다. 이 실패는 남은 실행을 마쳐도 동일 그룹에 포함해야 한다. 정상은 단일 통과만으로 채택할 수 없다. 남은 실행은 snapshot2→정상2→정상3→snapshot3 순서다.

`var/lower-rate-reference`는 두 실행 뒤 snapshot2 시작 전 유휴 검사에서 멈췄다. `var/lower-rate-resumed`, `var/lower-rate-resumed-2`는 그 두 원본을 참조하며 후속 workload를 시작하지 못했다. 마지막 재개에서는 관측 횟수만 15회로 늘렸고 busy 중앙값 15%·최대 30%·최소 10개 1초 관측 기준은 유지했다. 마지막 수집도 전부 gate 미달이어서 중단했다. 측정 실패를 교체하거나 완료한 두 실행을 재실행하지 않았다.

읽기 전용 프로세스 관측에서는 macOS mediaanalysisd와 Spotlight 계열의 CPU 사용이 높았다. 이 관찰로 유일한 원인을 확정하지 않으며 해당 프로세스를 중단·설정 변경하지 않았다. 사용자에게 부하를 줄일 수 있는지 질문한 상태다. gate를 만족하는 시점 또는 사용자가 정한 다른 실험 조건이 필요하다.

## 최적화 후보의 준비 상태

테스트 코드에서만 정수 JSON의 기존 nested json.Marshal과 직접 strconv.AppendInt를 비교했다. 음수의 ErrInteger와 최대 int64를 포함한 경계/고정 seed 1,000개에서 동일한 bytes/오류를 확인했다. production Int.MarshalJSON은 변경하지 않았다.

19자리 양수 하나를 반복한 CPU 1개·500ms·각 5회 단위 측정이다.

| 구현 | ns/op 중앙값 | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| 현재 | 130.8 | 80 | 4 |
| 테스트 전용 prototype | 20.50 | 24 | 1 |

값 크기·상위 JSON encoder·WAL/HTTP/GC·호스트 경쟁을 포함하는 전체 경로의 성능 개선으로 일반화하지 않는다. 원본은 `var/lower-rate-int-prototype-bench.txt`에 있다. 먼저 유효한 반복 기준을 확보한 뒤 적절한 조건에서 후보의 실제 적용 여부와 전체 경로 비교를 결정한다.

## 검증과 재개

Python 일반/`-O` 27개, portable Go 206개·native race 238개와 실제 거래/복구/비교/변조 smoke를 통과했다. 재개 회귀는 prefix 보존·부하가 시작된 부분 실패 교체 거절·조건/바이너리/원본 변경과 참조 chain 검사를 포함한다. 중단 parent 세 개와 연결 dependency의 inventory를 확인했다. feature source의 원격 CI와 master c9a9950의 CI를 구분한다.

환경이 준비되면 원래 producer와 원본을 그대로 사용해 다음 명령으로 남은 네 실행만 이어간다.

```sh
python3 tools/diagnostics/experiment.py baseline --build-dir var/lower-rate-build --output var/lower-rate-resumed-3 --preset reference --normal-rate 10000 --snapshot-rate 1000 --resume-from var/lower-rate-resumed-2 --quiet-attempts 15
```

이름은 새 경로여야 한다. resumeFrom으로 연결된 모든 parent를 보존한다. 이 기록은 작업 완료·성능 채택 보고서가 아니며 전원 장애 검증도 계속 보류다.
