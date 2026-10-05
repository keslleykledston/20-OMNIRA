import { useEffect, type ReactNode } from 'react'

const CONTACT_EMAIL = 'privacidade@k3gsolutions.com.br'
const UPDATED = '5 de outubro de 2026'

function Section({ id, title, children }: { id?: string; title: string; children: ReactNode }) {
  return (
    <section id={id} className="mt-8">
      <h2 className="text-lg font-semibold text-text-primary">{title}</h2>
      <div className="mt-2 space-y-3 text-sm leading-relaxed text-text-secondary">{children}</div>
    </section>
  )
}

/**
 * Public privacy policy (no login). Served at /privacidade; the URL goes into the Meta app settings, so it must stay
 * reachable without a session and must describe what OMNIRA really does with personal data.
 */
export default function PrivacyPolicyPage() {
  useEffect(() => {
    document.title = 'Política de Privacidade — OMNIRA'
  }, [])

  return (
    <main className="min-h-screen bg-surface">
      <div className="mx-auto max-w-3xl px-4 py-10 sm:px-6">
        <p className="text-xs font-semibold uppercase tracking-wide text-accent-primary">OMNIRA · K3G Solutions</p>
        <h1 className="mt-1 text-2xl font-semibold text-text-primary">Política de Privacidade</h1>
        <p className="mt-1 text-xs text-text-tertiary">Última atualização: {UPDATED}</p>

        <p className="mt-6 text-sm leading-relaxed text-text-secondary">
          O OMNIRA é a plataforma de atendimento da K3G Solutions: reúne em um só lugar as conversas que empresas têm com
          seus clientes por WhatsApp, além de chamados e cadastros de contatos. Esta política explica quais dados pessoais
          tratamos, para quê, com quem compartilhamos e como você exerce seus direitos, conforme a Lei Geral de Proteção
          de Dados (Lei nº 13.709/2018, LGPD).
        </p>

        <Section title="1. Quem é quem">
          <p>
            <strong>Empresas clientes (controladoras).</strong> Quando você conversa com uma empresa que usa o OMNIRA, é ela
            quem decide por que e como seus dados da conversa são usados. Nesse caso a K3G Solutions atua como{' '}
            <strong>operadora</strong>, tratando os dados em nome da empresa e segundo as instruções dela.
          </p>
          <p>
            <strong>K3G Solutions (controladora).</strong> Somos controladores dos dados das pessoas que usam a plataforma
            para atender (atendentes, supervisores e administradores) e dos dados necessários para operar, proteger e
            melhorar o serviço.
          </p>
        </Section>

        <Section title="2. Quais dados tratamos">
          <ul className="list-disc space-y-1.5 pl-5">
            <li>
              <strong>Contatos que falam com a empresa:</strong> número de telefone, nome informado no WhatsApp, nome e
              e-mail cadastrados pela equipe, anotações de atendimento e a empresa à qual o contato está vinculado.
            </li>
            <li>
              <strong>Conversas:</strong> texto das mensagens, horários, status de entrega e leitura, e arquivos enviados
              (imagens, áudios, vídeos e documentos).
            </li>
            <li>
              <strong>Atendentes e administradores:</strong> nome, e-mail, perfil de acesso, registros de uso e de ações
              realizadas (trilha de auditoria).
            </li>
            <li>
              <strong>Dados técnicos:</strong> endereço IP, tipo de navegador e registros de funcionamento, usados para
              segurança e diagnóstico.
            </li>
          </ul>
          <p>Não solicitamos dados pessoais sensíveis. Se você os enviar por conta própria numa conversa, eles ficam apenas no histórico daquele atendimento.</p>
        </Section>

        <Section title="3. Para que usamos (finalidades e bases legais)">
          <ul className="list-disc space-y-1.5 pl-5">
            <li>Receber, encaminhar e responder mensagens e registrar o atendimento — execução de contrato e legítimo interesse da empresa atendida.</li>
            <li>Identificar o contato e ligá-lo à empresa correta, abrir e acompanhar chamados — execução de contrato.</li>
            <li>Autenticar usuários, controlar permissões e manter trilha de auditoria — legítimo interesse e segurança.</li>
            <li>Detectar arquivos maliciosos e abuso (spam) — legítimo interesse e proteção do serviço.</li>
            <li>Cumprir obrigações legais e regulatórias e exercer direitos em processos.</li>
          </ul>
          <p>Não vendemos dados pessoais nem os usamos para publicidade de terceiros.</p>
        </Section>

        <Section title="4. WhatsApp e Meta">
          <p>
            O atendimento por WhatsApp acontece pela <strong>API oficial WhatsApp Business (Meta Cloud API)</strong> e,
            em algumas empresas, por uma sessão de WhatsApp conectada à plataforma. Ao usar o WhatsApp, seus dados também
            são tratados pela Meta Platforms conforme os termos e a política de privacidade do WhatsApp. O OMNIRA recebe
            da Meta apenas o que é necessário para o atendimento: número, nome de perfil, mensagens, arquivos e
            confirmações de entrega.
          </p>
          <p>As credenciais de acesso à Meta ficam armazenadas de forma cifrada e nunca são exibidas depois de salvas.</p>
        </Section>

        <Section title="5. Recursos de inteligência artificial (opcionais)">
          <p>
            A plataforma oferece recursos opcionais, ativados pela empresa cliente: resumo de conversas, transcrição de
            áudios e descrição de imagens. Quando ativados, o conteúdo necessário é enviado ao provedor de IA configurado
            pela empresa, somente para gerar o resultado pedido. Mensagens que pareçam instruções dirigidas à IA são
            tratadas como texto e nunca como comando. Cada empresa pode desligar esses recursos a qualquer momento.
          </p>
        </Section>

        <Section title="6. Com quem compartilhamos">
          <ul className="list-disc space-y-1.5 pl-5">
            <li>A <strong>empresa cliente</strong> com quem você conversa, que é a controladora dos dados do atendimento.</li>
            <li><strong>Meta Platforms</strong> (WhatsApp), para entregar e receber mensagens.</li>
            <li>Provedores de <strong>autenticação e e-mail</strong>, para entrar na plataforma e enviar convites.</li>
            <li>Provedores de <strong>inteligência artificial</strong> escolhidos pela empresa, nos casos descritos no item 5.</li>
            <li>Autoridades públicas, quando houver obrigação legal ou ordem judicial.</li>
          </ul>
          <p>Fornecedores atuam sob contrato e só tratam os dados para prestar o serviço contratado.</p>
        </Section>

        <Section title="7. Por quanto tempo guardamos">
          <p>
            Mantemos o histórico de atendimento enquanto a empresa cliente mantiver o contrato ativo ou pelo prazo que ela
            definir, e depois pelo tempo exigido em lei. Arquivos recebidos em conversas são mantidos por, no máximo, 60
            dias por padrão, salvo configuração diferente da empresa. Registros de auditoria são guardados pelo tempo
            necessário à segurança e à prestação de contas. Ao fim do prazo, os dados são excluídos ou anonimizados.
          </p>
        </Section>

        <Section title="8. Segurança">
          <p>
            Adotamos isolamento de dados entre empresas clientes, controle de acesso por perfil, credenciais cifradas,
            comunicação protegida por HTTPS, verificação de assinatura nas mensagens recebidas da Meta, varredura
            antivírus dos arquivos recebidos e registro das ações relevantes. Nenhum sistema é totalmente imune a
            incidentes; em caso de incidente que possa causar risco relevante, comunicaremos os envolvidos e a ANPD nos
            termos da lei.
          </p>
        </Section>

        <Section title="9. Seus direitos">
          <p>Nos termos do art. 18 da LGPD, você pode solicitar:</p>
          <ul className="list-disc space-y-1.5 pl-5">
            <li>confirmação de que tratamos seus dados e acesso a eles;</li>
            <li>correção de dados incompletos, inexatos ou desatualizados;</li>
            <li>anonimização, bloqueio ou eliminação de dados desnecessários ou tratados em desconformidade;</li>
            <li>portabilidade, informação sobre compartilhamento e sobre a possibilidade de não consentir;</li>
            <li>revogação de consentimento, quando essa for a base legal.</li>
          </ul>
          <p>
            Se o seu pedido se referir a uma conversa com uma empresa específica, encaminharemos a ela, que é a
            controladora dos dados. Você também pode reclamar à Autoridade Nacional de Proteção de Dados (ANPD).
          </p>
        </Section>

        <Section id="exclusao" title="10. Como pedir a exclusão dos seus dados">
          <p>
            Envie um e-mail para <a className="text-accent-primary underline" href={`mailto:${CONTACT_EMAIL}`}>{CONTACT_EMAIL}</a>{' '}
            com o assunto “Exclusão de dados” e o número de telefone usado no WhatsApp (com DDI e DDD). Confirmaremos o
            pedido, verificaremos que o número é seu e responderemos em até 15 dias. Dados que a lei nos obrigue a manter
            serão preservados apenas pelo prazo legal.
          </p>
        </Section>

        <Section title="11. Cookies">
          <p>
            Usamos apenas cookies e armazenamento local estritamente necessários para manter sua sessão e preferências de
            tela. Não usamos cookies de publicidade.
          </p>
        </Section>

        <Section title="12. Alterações desta política">
          <p>
            Podemos atualizar esta política para refletir mudanças no serviço ou na lei. A data da última atualização fica
            no topo desta página.
          </p>
        </Section>

        <Section title="13. Contato">
          <p>
            K3G Solutions — Privacidade e proteção de dados:{' '}
            <a className="text-accent-primary underline" href={`mailto:${CONTACT_EMAIL}`}>{CONTACT_EMAIL}</a>
          </p>
        </Section>
      </div>
    </main>
  )
}
