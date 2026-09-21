#!/usr/bin/env python3
"""run_evals.py — critério de ativação para ROUTER_MODE=active.

Roda hard_filter.py (a parte determinística, sem LLM) contra
evals/dataset.jsonl e verifica:

  1. 100% dos casos "security_critical*" resultam em FRONTIER_LLM com
     bypass_jev=true. Isto é GATE DE ATIVAÇÃO — qualquer falha aqui bloqueia
     ROUTER_MODE=active incondicionalmente, mesmo com 1 caso.
  2. 100% dos casos "deterministic" resultam em candidates == ["TOOL"].
  3. Casos "low_risk"/"general"/"unclassified" batem no expected_candidates.

Isto cobre só o filtro determinístico (hard_filter.py) — é a parte que
importa mais para segurança, porque roda sempre, mesmo se Jev estiver
indisponível ou errado. A qualidade do Jev (quando ele decide) deve ser
avaliada separadamente a partir do log real de logs/decisions.jsonl
acumulado em produção (shadow), comparando route_recommended com
actual_tier/test_result — ver README.md "Critérios de ativação".

Exit code 0 = gate passou. Exit code 1 = gate falhou (não ativar).

Uso:
  python3 .agents/router/evals/run_evals.py
"""
import json
import subprocess
import sys
from pathlib import Path

ROUTER_DIR = Path(__file__).parent.parent
DATASET = Path(__file__).parent / "dataset.jsonl"


def run_hard_filter(domains: str) -> dict:
    result = subprocess.run(
        [sys.executable, str(ROUTER_DIR / "hard_filter.py"), "--domains", domains],
        capture_output=True, text=True, check=False,
    )
    return json.loads(result.stdout)


def main():
    cases = [json.loads(line) for line in DATASET.read_text().splitlines() if line.strip()]

    failures = []
    security_gate_failures = []

    for case in cases:
        out = run_hard_filter(case["domains"])
        category = case.get("category", "")

        if category.startswith("security_critical"):
            ok = out.get("forced_tier") == "FRONTIER_LLM" and out.get("bypass_jev") is True
            if not ok:
                security_gate_failures.append({"case": case["id"], "domains": case["domains"], "got": out})
            continue

        if category == "deterministic":
            ok = out.get("candidates") == ["TOOL"]
            if not ok:
                failures.append({"case": case["id"], "domains": case["domains"], "got": out})
            continue

        if category == "design":
            ok = out.get("candidates") == [case["expected_tier"]]
            if not ok:
                failures.append({"case": case["id"], "domains": case["domains"], "got": out})
            continue

        expected_candidates = case.get("expected_candidates")
        if expected_candidates is not None:
            ok = out.get("candidates") == expected_candidates
            if not ok:
                failures.append({"case": case["id"], "domains": case["domains"], "expected": expected_candidates, "got": out})

    print(f"Casos avaliados: {len(cases)}")
    print(f"Security-critical gate failures: {len(security_gate_failures)}")
    print(f"Outras falhas: {len(failures)}")

    if security_gate_failures:
        print("\n!!! GATE DE SEGURANÇA FALHOU — bloqueando ativação incondicionalmente !!!")
        for f in security_gate_failures:
            print(f"  {f}")

    if failures:
        print("\nFalhas em outras categorias:")
        for f in failures:
            print(f"  {f}")

    if security_gate_failures or failures:
        print("\nRESULT: FAIL — não ativar ROUTER_MODE=active")
        sys.exit(1)

    print("\nRESULT: PASS (filtro determinístico) — ver README.md para os demais critérios de ativação (evidência de shadow log)")
    sys.exit(0)


if __name__ == "__main__":
    main()
