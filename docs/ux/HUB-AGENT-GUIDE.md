# Hub — guia do agente (workspace)

Estado: `LAB`. Layout aprovado em `HUB-LAYOUT-CONTRACT.md`.

1. **Conversas é a única caixa.** Se você atende 2+ empresas (instâncias), a lista junta todas, com o logo do canal e a etiqueta da empresa (código curto + nome por extenso). Há abas **Todas** e uma por instância; a primeira é a visão agregada.
2. **Fila × histórico.** A fila (pendentes) fica separada do histórico; use as abas Abertas/Aguardando/Minhas/Encerradas e a busca. O selo no item "Conversas" do menu conta quem está aguardando resposta nas instâncias do Hub.
3. **Tempo real.** A lista se atualiza assim que o Hub avisa que algo mudou (sem conteúdo no aviso); se o aviso cair, a lista segue pelo ciclo de 30 s.
4. **Assumir, responder, transferir.** Assumir pede a chave de resposta; responder grava na instância certa. Transferir mostra só quem pode responder aquela instância. Finalizar exige estar com a conversa e a chave `conversation.finalize`.
5. **Atender uma instância por inteiro.** Abra a conversa: a caixa completa (mídia, cartão do cliente, anotações, histórico, chamados) aparece conforme as chaves que o Hub liberou para você; o que não foi liberado não aparece nem como botão desligado enganoso.
6. **Abas de atendimento** sobrevivem à recarga (só os ids ficam guardados no navegador); ao sair, abas, rascunhos e cache são apagados.
7. **Contatos internos** só aparecem a quem tem `conversation.read_internal` (administrador/supervisor da instância).
8. Se algo sumiu: pode ter sido revogado — peça ao administrador do Hub; nada é apagado.
