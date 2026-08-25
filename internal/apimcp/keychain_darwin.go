//go:build darwin && cgo

package apimcp

import (
	"fmt"

	keychain "github.com/keybase/go-keychain"
)

type macOSKeychainStore struct{}

func platformKeychainStore() KeychainStore {
	return macOSKeychainStore{}
}

func (macOSKeychainStore) Get(ref KeychainConfig) ([]byte, error) {
	value, err := keychain.GetGenericPassword(ref.Service, ref.Account, "", "")
	if err != nil {
		return nil, fmt.Errorf("query macOS Keychain: %w", err)
	}
	if len(value) == 0 {
		return nil, ErrKeychainItemNotFound
	}
	return value, nil
}

func (macOSKeychainStore) Set(ref KeychainConfig, value []byte) error {
	query := keychain.NewItem()
	query.SetSecClass(keychain.SecClassGenericPassword)
	query.SetService(ref.Service)
	query.SetAccount(ref.Account)

	update := keychain.NewItem()
	update.SetData(value)
	if err := keychain.UpdateItem(query, update); err == nil {
		return nil
	} else if err != keychain.ErrorItemNotFound {
		return fmt.Errorf("update macOS Keychain item: %w", err)
	}

	item := keychain.NewGenericPassword(ref.Service, ref.Account, ref.Service, value, "")
	item.SetSynchronizable(keychain.SynchronizableNo)
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	if err := keychain.AddItem(item); err != nil {
		return fmt.Errorf("add macOS Keychain item: %w", err)
	}
	return nil
}

func (macOSKeychainStore) Delete(ref KeychainConfig) error {
	err := keychain.DeleteGenericPasswordItem(ref.Service, ref.Account)
	if err == nil || err == keychain.ErrorItemNotFound {
		return nil
	}
	return fmt.Errorf("delete macOS Keychain item: %w", err)
}
