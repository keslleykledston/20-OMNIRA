# 10 AUTOMATION EDITOR

**Rota:** `/app/automation/[flowId]`  
**Referência visual:** `../references/11-automation-editor.png`  
**Papéis:** Admin; Supervisor se autorizado.

## Propósito

Editor estruturado de fluxos sem depender de canvas drag-and-drop complexo no MVP.

## Wireframe

```text
APP SHELL
└─ EDITOR
   ├─ Header: name/status/save/publish
   └─ Split view
      ├─ Flow column
      │  ├─ Flow / Execution logs tabs
      │  ├─ zoom/fit controls
      │  └─ ordered step cards connected vertically
      └─ Properties panel
         ├─ selected step header
         ├─ properties tab
         └─ exit conditions tab
```

## Layout e regiões

- Split ~65/35.
- Flow usa cards de largura ~480–560 px centralizados na coluna.
- Conectores verticais simples; botão `+` entre steps.
- Step selecionado tem outline azul.
- Painel de propriedades branco com border/radius e scroll independente.

## Funcionalidades e interações

Nós MVP: Message, Condition, CaptureInput, SetVariable, ToolCall, HumanHandoff, End.  
Selecionar step abre properties.  
Adicionar step via `+`; reorder simples se suportado.  
Salvar rascunho. Publicar cria versão imutável.  
Logs de execução em tab separada.  
Inserção de variáveis usa picker, não texto mágico sem validação.

## Dados necessários

Flow, version, steps ordered/graph, selectedStep, variable schema, tool definitions, validation errors, publish status. Não acoplar UI a NATS runtime.

## Estados obrigatórios

Unsaved changes, validation errors, publishing, publish failed, read-only published version, permission denied. Antes de sair com dirty state, confirmar.

## Responsividade

Tablet: painel de propriedades em drawer/sheet. Mobile: editor não precisa reproduzir split; step list + properties full-screen sequencial.

## Component map conceitual

`FlowStepCard`, `FlowConnector`, `StepPicker`, `PropertiesPanel`, `Tabs`, `VariablePicker`, `StatusBadge`, `Button`, `ValidationSummary`.

## Checklist de fidelidade

- fluxo vertical simples como referência;
- cards homogêneos e conectores claros;
- painel direito não parece IDE pesada;
- seleção azul e status rascunho/publicação claros.
