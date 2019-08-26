package grpc_zerolog

func mergeFields(maps ...map[string]interface{}) map[string]interface{} {
	res := make(map[string]interface{})
	for _, m := range maps {
		for k, v := range m {
			res[k] = v
		}
	}
	return res
}
