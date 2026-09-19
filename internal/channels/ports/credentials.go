package ports

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Credential — segredo de um provider (access token, API key, etc.),
// mantido em memória só pelo tempo necessário para uma chamada ao
// provider externo. Nunca deve ser logado, serializado em resposta HTTP,
// colocado em mensagem NATS, ou devolvido ao frontend.
//
// Fields é um mapa em vez de uma struct fortemente tipada porque cada
// provider tem um conjunto diferente de campos sensíveis (Meta Cloud usa
// access_token; um provider session-based pode usar session_token +
// device_id). O NOME dos campos esperados é documentado por cada adapter
// de provider, não pelo domínio.
type Credential struct {
	Fields map[string]string
}

// String — Credential nunca imprime seus valores, mesmo em %v/%+v ou logs
// que chamem fmt implicitamente. Isso é a rede de segurança final: mesmo
// que um valor de Credential vaze para um log.Printf ou err de teste por
// engano, o texto impresso não contém o segredo.
func (c Credential) String() string {
	return fmt.Sprintf("Credential{%d campo(s) redigido(s)}", len(c.Fields))
}

// GoString — cobre também %#v (usado por alguns loggers/debuggers).
func (c Credential) GoString() string {
	return c.String()
}

// CredentialCipher — abstrai o mecanismo de criptografia em repouso (ver
// ADR-0009). Trocar a implementação (ex.: para um KMS externo) nunca deve
// exigir mudar CredentialStore ou qualquer código acima dele.
type CredentialCipher interface {
	// Encrypt retorna ciphertext pronto para persistir; deve gerar um
	// nonce novo a cada chamada (nunca reaproveitar nonce entre segredos).
	Encrypt(plaintext []byte) (ciphertext []byte, err error)
	Decrypt(ciphertext []byte) (plaintext []byte, err error)
}

// CredentialStore — persiste e resolve credenciais de conexão. O
// SecretRef devolvido por Store é o único dado que sai deste pacote para
// ser guardado em ChannelConnection.SecretRef — o valor de Credential em
// si nunca deixa o processo do servidor/worker que o resolveu por último.
type CredentialStore interface {
	// Store — criptografa e persiste credential, associado à connectionID.
	// Retorna o SecretRef (UUID da linha) a ser gravado em
	// ChannelConnection.SecretRef.
	Store(ctx context.Context, connectionID uuid.UUID, credential Credential) (secretRef string, err error)

	// Resolve — descriptografa e retorna a credencial associada a este
	// secretRef. Chamado exclusivamente server-side (adapter de provider
	// dentro do worker/API), nunca por um handler que responda ao
	// frontend.
	Resolve(ctx context.Context, secretRef string) (Credential, error)

	// Rotate — substitui a credencial de uma connection existente,
	// mantendo o mesmo SecretRef (evita ter que atualizar
	// ChannelConnection.SecretRef a cada rotação de token).
	Rotate(ctx context.Context, secretRef string, credential Credential) error
}
