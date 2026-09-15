package checker

import "regexp"

func ValidatePattern(pattern string) error {
	_, err := regexp.Compile(pattern)
	return err
}
