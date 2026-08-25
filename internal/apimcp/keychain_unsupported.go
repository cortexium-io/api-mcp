//go:build !darwin || !cgo

package apimcp

type unsupportedKeychainStore struct{}

func platformKeychainStore() KeychainStore {
	return unsupportedKeychainStore{}
}

func (unsupportedKeychainStore) Get(KeychainConfig) ([]byte, error) {
	return nil, ErrKeychainUnsupported
}

func (unsupportedKeychainStore) Set(KeychainConfig, []byte) error {
	return ErrKeychainUnsupported
}

func (unsupportedKeychainStore) Delete(KeychainConfig) error {
	return ErrKeychainUnsupported
}
