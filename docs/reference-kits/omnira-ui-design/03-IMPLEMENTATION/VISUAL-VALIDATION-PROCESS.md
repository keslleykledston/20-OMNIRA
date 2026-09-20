# Processo de validação visual

## 1. Fixar viewport

Usar o viewport da referência quando possível. As referências novas têm ~1448×1086; crops antigos variam. Para implementação, validar no mínimo desktop 1440×1080, 1280×800, tablet e mobile.

## 2. Screenshot por tela

Gerar screenshot da rota com fixtures estáveis. Nome sugerido:

```text
artifacts/ui/<screen>/implementation.png
```

## 3. Comparar

Comparar lado a lado com `references/<screen>.png`.

### Macro primeiro

- sidebar width;
- page padding;
- grid/columns;
- card sizes;
- table density;
- split ratios.

### Micro depois

- font size/weight;
- gaps;
- border/radius;
- shadow;
- icon scale;
- badge height;
- colors.

## 4. Registrar diferenças aceitas

Criar nota curta apenas quando houver diferença intencional por acessibilidade, arquitetura ou dado real.

## 5. Responsive gate

Depois do desktop: validar tablet e mobile segundo a spec da tela.

## 6. Não fazer pixel chasing inútil

Fidelidade significa mesma linguagem visual, hierarquia e proporção. Não sacrificar semântica/acessibilidade para copiar artefato acidental do mock.
