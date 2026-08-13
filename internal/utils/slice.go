package utils

// AppendUnique appends v when it is not already in list.
func AppendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
