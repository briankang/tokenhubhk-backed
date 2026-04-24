import mysql.connector
import json

def fix_qwen_features():
    db_config = {
        "host": "127.0.0.1",
        "user": "root",
        "password": "root",
        "database": "tokenhub"
    }

    try:
        conn = mysql.connector.connect(**db_config)
        cursor = conn.cursor(dictionary=True)

        print("--- Updating Supplier: aliyun_dashscope ---")
        default_features = {
            "supports_web_search": True,
            "supports_vision": True
        }
        cursor.execute(
            "UPDATE suppliers SET default_features = %s WHERE code = %s",
            (json.dumps(default_features), "aliyun_dashscope")
        )
        print(f"Updated {cursor.rowcount} supplier rows.")

        print("\n--- Updating Qwen Models ---")
        # Find all qwen/qwq/qvq models
        cursor.execute("SELECT id, model_name, features FROM ai_models WHERE model_name LIKE 'qwen%' OR model_name LIKE 'qwq%' OR model_name LIKE 'qvq%'")
        models = cursor.fetchall()

        updated_count = 0
        for m in models:
            name = m['model_name'].lower()
            features = {}
            if m['features']:
                try:
                    features = json.loads(m['features'])
                except:
                    features = {}

            dirty = False
            
            # Web Search: all qwen models
            if "supports_web_search" not in features:
                features["supports_web_search"] = True
                dirty = True
            
            # Thinking: qwen3 plus/max, qwq, qvq
            is_thinking = ("qwen3" in name and ("plus" in name or "max" in name)) or ("qwq" in name) or ("qvq" in name)
            if is_thinking:
                if features.get("supports_thinking") != True:
                    features["supports_thinking"] = True
                    dirty = True
            
            if dirty:
                cursor.execute(
                    "UPDATE ai_models SET features = %s WHERE id = %s",
                    (json.dumps(features), m['id'])
                )
                updated_count += 1
                print(f"Updated features for model: {m['model_name']}")

        conn.commit()
        print(f"\nSuccessfully updated {updated_count} models in the database.")

    except Exception as e:
        print(f"Error: {e}")
    finally:
        if 'conn' in locals() and conn.is_connected():
            cursor.close()
            conn.close()

if __name__ == "__main__":
    fix_qwen_features()
