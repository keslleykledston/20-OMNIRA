#!/usr/bin/env python3
"""analyze_shadow.py — Analisar logs/decisions.jsonl coletado em ROUTER_MODE=shadow.

Gera relatório por classe de tarefa, comparando rotas recomendadas vs executadas,
calculando taxa de sucesso, economia, latência, e gerando recomendações de
ativação gradual.

Saída: JSON estruturado + markdown summary em stdout.
Requer >= 50 decisões totais, >= 10 per classe com outcome registrado.

Uso:
  python3 .agents/router/analyze_shadow.py [--log-file decisions.jsonl]
"""
import json
import statistics
import sys
from collections import defaultdict
from datetime import datetime
from pathlib import Path

import yaml

ROUTER_DIR = Path(__file__).parent
LOG_FILE = ROUTER_DIR / "logs" / "decisions.jsonl"
POLICY_FILE = ROUTER_DIR / "policy.yaml"
MODELS_FILE = ROUTER_DIR / "models.yaml"


def load_logs(log_path):
    """Ler logs/decisions.jsonl e separa decisões de outcomes."""
    decisions = {}  # task_id -> decision
    outcomes = {}   # task_id -> outcome
    with open(log_path, "r", encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            entry = json.loads(line)
            task_id = entry.get("task_id")
            if entry.get("kind") == "outcome":
                outcomes[task_id] = entry
            else:
                decisions[task_id] = entry
    return decisions, outcomes


def get_task_class(decision, policy):
    """Deduzir task_class de uma decisão baseado em domain_tags."""
    domain_tags = decision.get("domain_tags", "").split(",")
    task_classes = policy.get("task_classes", {})

    # Tentar match por domain tag
    for class_name, config in task_classes.items():
        class_domains = set(config.get("domains", []))
        if class_domains & set(d.strip() for d in domain_tags if d.strip()):
            return class_name

    return "unclassified"


def analyze_per_class(decisions, outcomes, policy, models):
    """Gerar estatísticas por classe."""
    task_classes = policy.get("task_classes", {})
    risk_levels = models.get("eval_configuration", {}).get("class_risk_levels", {})

    results = defaultdict(lambda: {
        "decisions": 0,
        "outcomes": 0,
        "passed": 0,
        "failed": 0,
        "latencies_ms": [],
        "costs_usd": [],
        "overrides": 0,
        "fallbacks": 0,
        "jev_confidence_scores": [],
    })

    for task_id, decision in decisions.items():
        class_name = get_task_class(decision, policy)
        outcome = outcomes.get(task_id)

        results[class_name]["decisions"] += 1

        if outcome:
            results[class_name]["outcomes"] += 1
            if outcome.get("test_result") == "pass":
                results[class_name]["passed"] += 1
            elif outcome.get("test_result") == "fail":
                results[class_name]["failed"] += 1

            latency = outcome.get("latency_ms")
            if latency:
                results[class_name]["latencies_ms"].append(latency)

            cost = outcome.get("estimated_cost_usd", 0)
            results[class_name]["costs_usd"].append(cost)

            if outcome.get("human_override"):
                results[class_name]["overrides"] += 1

            if outcome.get("fallback_reason"):
                results[class_name]["fallbacks"] += 1

        jev_conf = decision.get("jev_confidence")
        if jev_conf is not None:
            results[class_name]["jev_confidence_scores"].append(jev_conf)

    # Calcular métricas derivadas
    for class_name, stats in results.items():
        outcomes_n = stats["outcomes"]
        if outcomes_n > 0:
            stats["success_rate"] = stats["passed"] / outcomes_n
            stats["override_rate"] = stats["overrides"] / outcomes_n
            stats["fallback_rate"] = stats["fallbacks"] / outcomes_n
        else:
            stats["success_rate"] = None
            stats["override_rate"] = None
            stats["fallback_rate"] = None

        if stats["latencies_ms"]:
            stats["latency_median_ms"] = statistics.median(stats["latencies_ms"])
            stats["latency_mean_ms"] = statistics.mean(stats["latencies_ms"])

        if stats["costs_usd"]:
            stats["cost_total_usd"] = sum(stats["costs_usd"])

        if stats["jev_confidence_scores"]:
            stats["jev_confidence_median"] = statistics.median(stats["jev_confidence_scores"])

    return results, risk_levels


def activation_recommendation(class_name, stats, thresholds, risk_level):
    """Recomendar ativação baseado em stats vs thresholds."""
    if stats["outcomes"] == 0:
        return "INSUFFICIENT_DATA", "Sem outcomes registrados."

    if stats["outcomes"] < 10:
        return "COLLECT_MORE", f"Apenas {stats['outcomes']} outcomes, mínimo é 10."

    if risk_level in ["security_critical"]:
        return "FORCED_HUMAN", "Security-critical — nunca auto-active."

    if risk_level == "deterministic":
        if stats["success_rate"] >= 0.95:
            return "READY_ACTIVE", "Determinístico (TOOL) — zero risco."
        else:
            return "INVESTIGATE", f"Sucesso {stats['success_rate']:.1%} < 95% (determinístico deveria ser 100%)"

    # Para simples e ambíguo, aplicar thresholds
    success_rate = stats.get("success_rate")
    override_rate = stats.get("override_rate")
    fallback_rate = stats.get("fallback_rate")

    if success_rate is None or success_rate < thresholds["success_rate"]:
        return "CONTINUE_SHADOW", f"Sucesso {success_rate:.1%} < {thresholds['success_rate']:.0%}."

    if override_rate and override_rate > thresholds["human_override_rate"]:
        return "INVESTIGATE", f"Override rate {override_rate:.1%} > {thresholds['human_override_rate']:.0%}."

    if fallback_rate and fallback_rate > thresholds["fallback_rate_max"]:
        return "INVESTIGATE", f"Fallback rate {fallback_rate:.1%} > {thresholds['fallback_rate_max']:.0%}."

    return "READY_ACTIVE", "Atende todos os thresholds."


def main():
    log_path = Path(sys.argv[1] if len(sys.argv) > 1 else LOG_FILE)
    if not log_path.exists():
        print(f"Log não encontrado: {log_path}", file=sys.stderr)
        sys.exit(1)

    policy = yaml.safe_load(open(POLICY_FILE))
    models = yaml.safe_load(open(MODELS_FILE))

    decisions, outcomes = load_logs(log_path)

    if len(decisions) < 50:
        print(f"⚠️  Apenas {len(decisions)} decisões coletadas (mínimo: 50). Dados insuficientes para análise.", file=sys.stderr)

    results, risk_levels = analyze_per_class(decisions, outcomes, policy, models)
    thresholds = models["eval_configuration"]["activation_thresholds"]

    # Organizar por risco
    risk_map = {}
    for risk, classes in risk_levels.items():
        for cls in classes:
            risk_map[cls] = risk

    # Agrupar recomendações
    recommendations = {}
    for class_name in sorted(results.keys()):
        stats = results[class_name]
        risk = risk_map.get(class_name, "unclassified")
        rec, reason = activation_recommendation(class_name, stats, thresholds, risk)
        if rec not in recommendations:
            recommendations[rec] = []
        recommendations[rec].append((class_name, reason))

    # Markdown output
    print("# ROUTER.3 Shadow Evaluation Report")
    print(f"\n**Gerado:** {datetime.now().isoformat()}")
    print(f"**Decisões totais:** {len(decisions)}")
    print(f"**Com outcome:** {len(outcomes)} ({len(outcomes)/len(decisions)*100:.0f}%)")
    print(f"**Classes:** {len(results)}")

    print("\n## Per-Class Breakdown\n")
    for class_name in sorted(results.keys()):
        stats = results[class_name]
        risk = risk_map.get(class_name, "?")
        print(f"### {class_name} ({risk})")
        print(f"- Decisões: {stats['decisions']} | Outcomes: {stats['outcomes']}")
        if stats["success_rate"] is not None:
            print(f"- Sucesso: {stats['success_rate']:.1%} | Override: {stats['override_rate']:.1%} | Fallback: {stats['fallback_rate']:.1%}")
        if stats.get("latency_median_ms"):
            print(f"- Latência mediana: {stats['latency_median_ms']:.0f}ms")
        if stats.get("cost_total_usd"):
            print(f"- Custo total: ${stats['cost_total_usd']:.2f}")
        print()

    print("\n## Recomendações de Ativação\n")
    for rec_status in ["READY_ACTIVE", "INVESTIGATE", "CONTINUE_SHADOW", "COLLECT_MORE", "INSUFFICIENT_DATA", "FORCED_HUMAN"]:
        if rec_status not in recommendations:
            continue
        items = recommendations[rec_status]
        print(f"### {rec_status} ({len(items)})\n")
        for class_name, reason in items:
            print(f"- **{class_name}:** {reason}")
        print()

    # JSON output para programmatic use
    report = {
        "timestamp": datetime.now().isoformat(),
        "total_decisions": len(decisions),
        "total_outcomes": len(outcomes),
        "per_class": {cls: dict(stats) for cls, stats in results.items()},  # dicts não-serializable removidas
        "recommendations": {rec: list(classes) for rec, classes in recommendations.items()},
    }
    print("\n---\n")
    print(json.dumps(report, indent=2, default=str))


if __name__ == "__main__":
    main()
