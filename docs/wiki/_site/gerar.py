#!/usr/bin/env python3
"""Gera a página da wiki (docs/wiki/_site/dist/index.html + imagens/) a partir dos Markdown de docs/wiki.

    python3 docs/wiki/_site/gerar.py

Uma página só (sem build de JS): o menu mostra uma seção por vez, a busca roda no navegador. O formato do arquivo segue o que o publicador de artefatos
espera: sem <html>/<head>/<body>, com <title> e <style> no topo.
"""
import html, json, re, shutil, sys
from datetime import date
from pathlib import Path
from markdown_it import MarkdownIt

RAIZ = Path(__file__).resolve().parents[1]
SAIDA = Path(__file__).resolve().parent / "dist"

# (arquivo, grupo do menu). A ordem é a do menu e a do "anterior/próximo".
PAGINAS = [
    ("README.md", "Início"),
    ("guias/01-visao-geral.md", "Primeiros passos"),
    ("tutoriais/01-atender-uma-conversa.md", "Tutoriais"),
    ("tutoriais/02-buscar-e-navegar-com-ctrl-k.md", "Tutoriais"),
    ("tutoriais/03-tickets-e-pessoas.md", "Tutoriais"),
    ("tutoriais/04-classificar-contatos-e-spam.md", "Tutoriais"),
    ("tutoriais/05-filas-e-setores.md", "Tutoriais"),
    ("tutoriais/06-conectar-um-crm-pelo-painel.md", "Tutoriais"),
    ("tutoriais/07-primeira-chamada-a-api.md", "Tutoriais"),
    ("integradores/guia-da-api.md", "Para integradores"),
    ("integradores/roteiro-de-teste.md", "Para integradores"),
    ("integradores/contrato-openapi.md", "Para integradores"),
    ("faq/FAQ.md", "Ajuda"),
    ("GLOSSARIO.md", "Ajuda"),
    ("PLANO-DA-DOCUMENTACAO.md", "Sobre"),
]


def pid(arq: str) -> str:
    return "inicio" if arq == "README.md" else re.sub(r"\.md$", "", arq).replace("/", "-").lower()


IDS = {a: pid(a) for a, _ in PAGINAS}
md = MarkdownIt("commonmark").enable("table")


def resolve_link(origem: str, alvo: str) -> str:
    base, _, ancora = alvo.partition("#")
    if not base.endswith(".md"):
        return alvo
    destino = (Path(origem).parent / base)
    partes = []
    for p in destino.parts:
        if p == "..":
            if partes:
                partes.pop()
        elif p != ".":
            partes.append(p)
    chave = "/".join(partes)
    return "#" + IDS[chave] if chave in IDS else alvo


def renderiza(arq: str) -> tuple[str, str, str]:
    texto = (RAIZ / arq).read_text(encoding="utf-8")
    tokens = md.parse(texto)
    titulo = ""
    for i, t in enumerate(tokens):
        if t.type == "heading_open" and t.tag == "h1" and not titulo:
            titulo = tokens[i + 1].content
        if t.type == "heading_open" and t.tag == "h1":
            t.tag = "h1"
        if t.type == "inline":
            for filho in t.children or []:
                if filho.type == "link_open":
                    h = filho.attrGet("href") or ""
                    filho.attrSet("href", resolve_link(arq, h))
                    if not filho.attrGet("href").startswith("#"):
                        filho.attrSet("target", "_blank")
                        filho.attrSet("rel", "noopener")
                if filho.type == "image":
                    src = filho.attrGet("src") or ""
                    filho.attrSet("src", re.sub(r"^(\.\./)+", "", src))
                    filho.attrSet("loading", "lazy")
        if t.type == "table_open":
            t.attrSet("class", "tabela")
    corpo = md.renderer.render(tokens, md.options, {})
    corpo = corpo.replace("<table", '<div class="rolagem"><table').replace("</table>", "</table></div>")
    corpo = re.sub(r"(<pre><code[^>]*>)", r"\1", corpo)
    if arq == "faq/FAQ.md":
        corpo = faq_em_acordeao(corpo)
    return titulo, corpo, texto


def faq_em_acordeao(corpo: str) -> str:
    """Cada pergunta (h3) vira um <details>; os h2 continuam como títulos de assunto."""
    saida, aberto = [], False
    for parte in re.split(r"(?=<h[23]>)", corpo):
        if parte.startswith("<h3>"):
            if aberto:
                saida.append("</div></details>")
            pergunta, _, resto = parte[4:].partition("</h3>")
            saida.append(f'<details class="pergunta"><summary>{pergunta}</summary><div class="resposta">{resto}')
            aberto = True
        else:
            if aberto:
                saida.append("</div></details>")
                aberto = False
            saida.append(parte)
    if aberto:
        saida.append("</div></details>")
    return "".join(saida)


def plano_de_texto(texto: str) -> str:
    t = re.sub(r"```.*?```", " ", texto, flags=re.S)
    t = re.sub(r"!\[[^\]]*\]\([^)]*\)", " ", t)
    t = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", t)
    t = re.sub(r"[#*`>|_-]+", " ", t)
    return re.sub(r"\s+", " ", t).strip()


def main() -> None:
    SAIDA.mkdir(exist_ok=True)
    img = SAIDA / "imagens"
    if img.exists():
        shutil.rmtree(img)
    shutil.copytree(RAIZ / "imagens", img)

    secoes, indice, nav = [], [], {}
    for arq, grupo in PAGINAS:
        titulo, corpo, texto = renderiza(arq)
        i = IDS[arq]
        nav.setdefault(grupo, []).append((i, titulo))
        indice.append({"id": i, "titulo": titulo, "grupo": grupo, "texto": plano_de_texto(texto)[:20000]})
        secoes.append(f'<section class="pagina" id="p-{i}" data-titulo="{html.escape(titulo)}" hidden><article>{corpo}</article></section>')

    ordem = [IDS[a] for a, _ in PAGINAS]
    menu = []
    for grupo, itens in nav.items():
        menu.append(f'<div class="grupo"><p class="grupo-nome">{html.escape(grupo)}</p>')
        for i, t in itens:
            curto = re.sub(r"^Tutorial \d+: ", lambda m: m.group(0).split(":")[0] + " · ", t)
            menu.append(f'<a class="item" href="#{i}" data-id="{i}">{html.escape(curto)}</a>')
        menu.append("</div>")

    modelo = (Path(__file__).resolve().parent / "modelo.html").read_text(encoding="utf-8")
    pagina = (modelo.replace("{{MENU}}", "\n".join(menu)).replace("{{SECOES}}", "\n".join(secoes))
              .replace("{{INDICE}}", json.dumps(indice, ensure_ascii=False).replace("</", "<\\/"))
              .replace("{{ORDEM}}", json.dumps(ordem)).replace("{{DATA}}", date.today().strftime("%d/%m/%Y")))
    (SAIDA / "index.html").write_text(pagina, encoding="utf-8")
    print(f"ok: {len(PAGINAS)} páginas, {len(list(img.glob('*.png')))} imagens, {len(pagina)//1024} KB em {SAIDA}")


if __name__ == "__main__":
    sys.exit(main())
