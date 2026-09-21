#!/usr/bin/env python3
"""hard_filter.py — filtragem determinística de política (SEM LLM).

Lê .agents/router/policy.yaml e, para os domain_tags de uma tarefa, decide:
  - forced_tier (se algum domain bater em hard_force_frontier) + bypass_jev
  - candidates permitidos (primeiro candidate_set cujo domain bater; senão default)

Este módulo NUNCA chama um modelo. É o gate que roda antes do Jev — Jev só
recebe os candidatos já restritos por aqui, e nunca decide para domínios
hard_force_frontier.

Uso:
  python3 hard_filter.py --domains auth,migration_check
  # -> JSON em stdout: {"forced_tier":"FRONTIER_LLM","bypass_jev":true,"candidates":["FRONTIER_LLM"],"reason":"..."}
"""
import argparse
import json
import sys
from pathlib import Path

import yaml

POLICY_PATH = Path(__file__).parent / "policy.yaml"


def load_policy():
    with open(POLICY_PATH, "r", encoding="utf-8") as f:
        return yaml.safe_load(f)


def evaluate(domains: list[str], policy: dict) -> dict:
    domains_set = set(d.strip() for d in domains if d.strip())

    hard = policy.get("hard_force_frontier", {})
    if domains_set & set(hard.get("domains", [])):
        return {
            "forced_tier": hard["forced_tier"],
            "bypass_jev": bool(hard.get("bypass_jev", True)),
            "candidates": [hard["forced_tier"]],
            "reason": hard.get("reason", "hard_force_frontier match"),
            "matched_rule": "hard_force_frontier",
        }

    matches = []
    for rule in policy.get("candidate_sets", []):
        if domains_set & set(rule.get("domains", [])):
            matches.append(rule)

    if len(matches) > 1:
        # Domain ambíguo entre regras incompatíveis: não adivinhar — humano decide.
        return {
            "forced_tier": None,
            "bypass_jev": True,
            "candidates": ["HUMAN"],
            "reason": "policy_conflict: domain_tags batem em mais de um candidate_set",
            "matched_rule": "policy_conflict",
        }

    if matches:
        rule = matches[0]
        return {
            "forced_tier": None,
            "bypass_jev": len(rule["candidates"]) <= 1,
            "candidates": rule["candidates"],
            "reason": rule.get("reason", ""),
            "matched_rule": "candidate_sets",
        }

    default = policy.get("default", {"candidates": ["TOOL", "LOCAL_LLM", "CHEAP_LLM", "FRONTIER_LLM"]})
    return {
        "forced_tier": None,
        "bypass_jev": False,
        "candidates": default["candidates"],
        "reason": default.get("reason", "no domain match, using default"),
        "matched_rule": "default",
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--domains", required=True, help="CSV de domain_tags, ex: auth,migration_check")
    args = parser.parse_args()

    policy = load_policy()
    domains = args.domains.split(",")
    result = evaluate(domains, policy)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # nunca deixar o filtro determinístico quebrar silenciosamente
        print(json.dumps({"error": str(exc), "candidates": ["HUMAN"], "bypass_jev": True, "forced_tier": None}), file=sys.stdout)
        sys.exit(1)
