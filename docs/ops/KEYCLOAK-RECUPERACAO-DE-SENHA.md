# Recuperação de senha pelo Keycloak (realm `omnira`)

Estado: **ligada em produção em 2026-10-18** (pedido do dono: a tela de login não tinha "Esqueceu a senha?").

## O que o usuário vê
Na tela de login (`auth.devops.k3gsolutions.com.br`, em português) há o link **Esqueceu sua senha?**. Ele pede o e-mail ou usuário, o Keycloak manda um **link que vale 15 minutos** para o e-mail cadastrado e a pessoa define uma senha nova (política do realm: 8 caracteres, maiúscula, minúscula e número). A resposta da tela é a mesma exista ou não a conta (não revela quem tem cadastro).

## O que foi alterado no realm (e por quê)
| Opção | Antes | Agora |
|---|---|---|
| `resetPasswordAllowed` | `false` | `true` (o link aparece) |
| `smtpServer` | vazio | o mesmo SMTP da K3G que o OMNIRA já usa para convites (`mail.k3gsolutions.com.br`, 587, STARTTLS, remetente "OMNIRA <autenticacao@...>"); sem e-mail não há recuperação |
| `actionTokenGeneratedByUserLifespan` | 300 s | 900 s (o link de redefinição vale 15 min) |
| `internationalizationEnabled` / `supportedLocales` | desligado | ligado, `pt-BR` e `en`; `defaultLocale` `pt-BR`. A tela de login e os e-mails saem em português |

Não mudou nenhum código do OMNIRA. Backup do realm **antes** da mudança: `~/.config/omnira/keycloak-realm-omnira-before-recovery-20261018T042635Z.json` (0600, fora do repositório).

`deploy/keycloak/omnira-realm.json` recebeu as opções acima (para um ambiente novo). O **SMTP não vai nesse arquivo** (tem senha): em ambiente novo configure em Realm settings > Email, ou com `kcadm` (receita abaixo).

## Prova feita
- A página de login devolve o link `reset-credentials` e o texto "Esqueceu sua senha?".
- Um e-mail de redefinição foi enviado pelo admin ao próprio dono (`execute-actions-email`, ação `UPDATE_PASSWORD`, validade 900 s) e o Keycloak aceitou o envio sem erro de SMTP.

## Como configurar o SMTP em outro ambiente (sem expor a senha)
A senha do admin do Keycloak e a do SMTP nunca vão em argumento de linha de comando. Monte o JSON em arquivo `0600` e use `kcadm.sh update realms/omnira -f arquivo.json` (o `--config` vai no fim do comando no kcadm 25). Receita completa de login do `kcadm` em `ops-facts` da memória do projeto.

## Como desfazer
`kcadm.sh update realms/omnira -s resetPasswordAllowed=false` (o link some). Para voltar tudo, reaplique o backup do realm (o SMTP e o idioma ficam como antes).

## Redefinir a senha de alguém (administrador)
No Keycloak (realm `omnira` > Users > a pessoa > Credentials > Reset password), ou pelo `kcadm` com a ação `UPDATE_PASSWORD` por e-mail (`users/<id>/execute-actions-email`, `lifespan=900`). Evite escolher a senha pela pessoa; mande o link.

## Ainda em aberto (recomendações, não feitas)
- **Proteção contra tentativa em massa (brute force) está desligada** no realm `omnira`. Com a recuperação ligada, vale ligar (Realm settings > Security defenses > Brute force detection) com limite de falhas por usuário e bloqueio temporário.
- **Eventos de login** do realm: estão em log, mas não há painel; considere guardar `LOGIN_ERROR` e `RESET_PASSWORD`.
- **Admin do Keycloak** (`/admin/` e `/realms/master/` no nginx público): continua alcançável de fora (item do dono).
